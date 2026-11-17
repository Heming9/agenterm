use crate::{
    protocol::{self, Address, Result, Token, VERSION},
    terminal::Manager,
    ui,
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD as B64};
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    io::{self, Read, Write},
    net::{TcpStream, ToSocketAddrs},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    time::{Duration, Instant},
};
use tungstenite::{
    client::IntoClientRequest,
    protocol::{Message, WebSocketConfig},
    stream::MaybeTlsStream,
};

fn tcp(a: &Address) -> Result<TcpStream> {
    let addresses = (a.host.as_str(), a.port)
        .to_socket_addrs()
        .map_err(|e| e.to_string())?;
    let mut last = "server has no addresses".to_string();
    for address in addresses {
        match TcpStream::connect_timeout(&address, Duration::from_secs(10)) {
            Ok(s) => {
                s.set_read_timeout(Some(Duration::from_secs(15)))
                    .map_err(|e| e.to_string())?;
                s.set_write_timeout(Some(Duration::from_secs(10)))
                    .map_err(|e| e.to_string())?;
                let _ = s.set_nodelay(true);
                return Ok(s);
            }
            Err(e) => last = e.to_string(),
        }
    }
    Err(last)
}
fn stream(a: &Address) -> Result<MaybeTlsStream<TcpStream>> {
    let tcp = tcp(a)?;
    if a.secure() {
        let mut builder = native_tls::TlsConnector::builder();
        if let Some(path) = std::env::var_os("AGENTERM_CA_FILE") {
            let certificate =
                native_tls::Certificate::from_pem(&std::fs::read(path).map_err(|e| e.to_string())?)
                    .map_err(|e| e.to_string())?;
            builder.add_root_certificate(certificate);
        }
        let tls = builder
            .build()
            .map_err(|e| e.to_string())?
            .connect(&a.host, tcp)
            .map_err(|e| e.to_string())?;
        Ok(MaybeTlsStream::NativeTls(tls))
    } else {
        Ok(MaybeTlsStream::Plain(tcp))
    }
}

pub fn redeem(a: &Address, secret: Option<&str>) -> Result<Token> {
    let mut stream = stream(a)?;
    let auth = secret
        .map(|s| format!("Authorization: Bearer {s}\r\n"))
        .unwrap_or_default();
    write!(stream,"POST {} HTTP/1.1\r\nHost: {}\r\nX-Agenterm-Version: {VERSION}\r\n{auth}Content-Length: 0\r\nConnection: close\r\n\r\n",a.path,a.authority).map_err(|e|e.to_string())?;
    // The endpoint returns at most a small JSON token. Bound headers and body;
    // redirects are rejected so credentials never follow an untrusted Location.
    let mut bytes = Vec::new();
    let mut block = [0; 2048];
    loop {
        let n = stream.read(&mut block).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        bytes.extend_from_slice(&block[..n]);
        if bytes.len() > 65536 {
            return Err("invitation response too large".into());
        }
    }
    let mut headers = [httparse::EMPTY_HEADER; 64];
    let mut response = httparse::Response::new(&mut headers);
    let start = match response.parse(&bytes).map_err(|e| e.to_string())? {
        httparse::Status::Complete(n) => n,
        _ => return Err("incomplete HTTP response".into()),
    };
    let status = response.code.unwrap_or(0);
    let header = |key: &str| {
        response
            .headers
            .iter()
            .find(|h| h.name.eq_ignore_ascii_case(key))
            .and_then(|h| std::str::from_utf8(h.value).ok())
            .unwrap_or("")
    };
    if status == 200 || status == 426 {
        protocol::check_version(header("X-Agenterm-Version"))?;
    }
    if status != 200 {
        return Err(format!("invitation or short code rejected (HTTP {status})"));
    }
    let body = if header("Transfer-Encoding").eq_ignore_ascii_case("chunked") {
        unchunk(&bytes[start..])?
    } else {
        let raw = &bytes[start..];
        if !header("Content-Length").is_empty()
            && header("Content-Length").parse::<usize>().ok() != Some(raw.len())
        {
            return Err("incomplete HTTP body".into());
        }
        raw.to_vec()
    };
    if body.len() > 4096 {
        return Err("token response too large".into());
    }
    let value: Value = serde_json::from_slice(&body).map_err(|e| e.to_string())?;
    Token::decode(value["token"].as_str().ok_or("missing connection token")?)
}
fn unchunk(mut data: &[u8]) -> Result<Vec<u8>> {
    let mut out = vec![];
    loop {
        let end = data
            .windows(2)
            .position(|w| w == b"\r\n")
            .ok_or("invalid chunk header")?;
        let text = std::str::from_utf8(&data[..end])
            .map_err(|_| "invalid chunk size")?
            .split(';')
            .next()
            .unwrap();
        let size = usize::from_str_radix(text, 16).map_err(|_| "invalid chunk size")?;
        data = &data[end + 2..];
        if size == 0 {
            return Ok(out);
        }
        if size > 4096 - out.len() || data.len() < size + 2 || &data[size..size + 2] != b"\r\n" {
            return Err("invalid chunk body".into());
        }
        out.extend_from_slice(&data[..size]);
        data = &data[size + 2..];
    }
}

struct Failure {
    message: String,
    permanent: bool,
    retry_after: u64,
    online: Duration,
}
impl From<String> for Failure {
    fn from(message: String) -> Self {
        Self {
            message,
            permanent: false,
            retry_after: 0,
            online: Duration::ZERO,
        }
    }
}
type Attempt = std::result::Result<(), Failure>;

struct IdleTracker {
    manager: Manager,
    started: Arc<AtomicBool>,
    done: Arc<AtomicBool>,
    worker: Option<std::thread::JoinHandle<()>>,
}

impl IdleTracker {
    fn new(manager: &Manager, stopped: &Arc<AtomicBool>, timeout: Option<Duration>) -> Self {
        let started = Arc::new(AtomicBool::new(false));
        let done = Arc::new(AtomicBool::new(false));
        let worker = timeout.map(|timeout| {
            let manager = manager.clone();
            let stopped = stopped.clone();
            let started = started.clone();
            let done = done.clone();
            std::thread::spawn(move || {
                while !done.load(Ordering::SeqCst) && !stopped.load(Ordering::SeqCst) {
                    if started.load(Ordering::SeqCst) && manager.idle_for() >= timeout {
                        manager.reporter().message(
                            "Disconnected after 5 minutes of inactivity.",
                            "连接已因 5 分钟无操作自动断开。",
                        );
                        stopped.store(true, Ordering::SeqCst);
                        break;
                    }
                    std::thread::sleep(Duration::from_millis(50));
                }
            })
        });
        Self {
            manager: manager.clone(),
            started,
            done,
            worker,
        }
    }

    fn connected(&self) {
        // Only the network loop calls this method. Publish started after touch
        // so a long initial connection attempt cannot immediately expire it.
        if !self.started.load(Ordering::SeqCst) {
            self.manager.touch();
            self.started.store(true, Ordering::SeqCst);
        }
    }
}

impl Drop for IdleTracker {
    fn drop(&mut self) {
        self.done.store(true, Ordering::SeqCst);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

pub fn run(
    t: &Token,
    manager: &Manager,
    stopped: &Arc<AtomicBool>,
    temporary: bool,
    invited: bool,
) -> Result<()> {
    let idle = IdleTracker::new(
        manager,
        stopped,
        temporary.then_some(Duration::from_secs(300)),
    );
    let mut resume = String::new();
    let mut delay = 1;
    let mut attempt = 0;
    while !stopped.load(Ordering::SeqCst) {
        manager.reporter().connecting();
        match once(t, manager, stopped, &idle, invited, &mut resume) {
            Ok(()) => return Ok(()),
            Err(e) if stopped.load(Ordering::SeqCst) => {
                let _ = e;
                return Ok(());
            }
            Err(e) if e.permanent => return Err(e.message),
            Err(e) => {
                if e.online.as_secs() >= 60 {
                    delay = 1;
                    attempt = 0;
                }
                attempt += 1;
                let wait = delay.max(e.retry_after).min(300);
                manager.reporter().retry(
                    if ui::zh() {
                        format!("连接断开或失败（已连接 {}）：{}。第 {attempt} 次重试将在 {wait}s 后开始。", duration_text(e.online), e.message)
                    } else {
                        format!("Connection lost or failed (online {}): {}. Retry #{attempt} in {wait}s.", duration_text(e.online), e.message)
                    }, wait,
                );
                let deadline = Instant::now() + Duration::from_secs(wait);
                while Instant::now() < deadline && !stopped.load(Ordering::SeqCst) {
                    std::thread::sleep(Duration::from_millis(50));
                }
                delay = (delay * 2).min(30);
            }
        }
    }
    Ok(())
}
fn once(
    t: &Token,
    manager: &Manager,
    stopped: &Arc<AtomicBool>,
    idle: &IdleTracker,
    invited: bool,
    resume: &mut String,
) -> Attempt {
    let mut request = t
        .connection_url()
        .into_client_request()
        .map_err(|e| e.to_string())?;
    let headers = request.headers_mut();
    let mut add = |k: &'static str, v: &str| -> Result<()> {
        headers.insert(k, v.parse().map_err(|_| "invalid connection header")?);
        Ok(())
    };
    add("authorization", &format!("Bearer {}", t.secret))?;
    add("x-agenterm-version", VERSION)?;
    add("x-agenterm-client-api", "1")?;
    add(
        "x-agenterm-platform",
        if cfg!(target_os = "macos") {
            "darwin"
        } else {
            std::env::consts::OS
        },
    )?;
    add(
        "x-agenterm-arch",
        match std::env::consts::ARCH {
            "x86_64" => "amd64",
            "aarch64" => "arm64",
            x => x,
        },
    )?;
    add(
        "x-agenterm-hostname",
        &B64.encode(crate::terminal::hostname()),
    )?;
    add("x-agenterm-shell", &B64.encode(crate::terminal::shell()))?;
    add(
        "x-agenterm-default-cwd",
        &B64.encode(
            std::env::current_dir()
                .map_err(|e| e.to_string())?
                .to_string_lossy()
                .as_bytes(),
        ),
    )?;
    add(
        "x-agenterm-session-controls",
        if cfg!(unix) { "interrupt,eof" } else { "eof" },
    )?;
    // P2P is deliberately not advertised until bootstrap lifecycle is ported.
    if !resume.is_empty() {
        add("x-agenterm-resume-credential", resume)?;
    }
    let socket = stream(&t.address)?;
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    let config = WebSocketConfig::default()
        .max_message_size(Some(2 << 20))
        .max_frame_size(Some(2 << 20));
    let (mut ws, response) = tungstenite::client::client_with_config(request, socket, Some(config))
        .map_err(|e| {
            if let tungstenite::HandshakeError::Failure(tungstenite::Error::Http(r)) = &e {
                let status = r.status().as_u16();
                return Failure {
                    message: format!("server rejected connection (HTTP {status})"),
                    permanent: !(status == 408 || status == 429 || status >= 500),
                    online: Duration::ZERO,
                    retry_after: r
                        .headers()
                        .get("retry-after")
                        .and_then(|v| v.to_str().ok())
                        .and_then(|v| v.parse().ok())
                        .unwrap_or(0),
                };
            }
            Failure::from(e.to_string())
        })?;
    if stopped.load(Ordering::SeqCst) {
        let _ = ws.close(None);
        return Ok(());
    }
    let header = |name: &str| {
        response
            .headers()
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    };
    protocol::check_version(header("x-agenterm-version")).map_err(|message| Failure {
        message,
        permanent: true,
        retry_after: 0,
        online: Duration::ZERO,
    })?;
    if header("x-agenterm-client-api") != "1" {
        return Err(Failure {
            message: "server requires a different client API".into(),
            permanent: true,
            retry_after: 0,
            online: Duration::ZERO,
        });
    }
    *resume = header("x-agenterm-resume-credential").into();
    let heartbeat = header("x-agenterm-heartbeat") == "1";
    match ws.get_mut() {
        MaybeTlsStream::Plain(s) => s.set_read_timeout(Some(Duration::from_millis(50))),
        MaybeTlsStream::NativeTls(s) => s
            .get_ref()
            .set_read_timeout(Some(Duration::from_millis(50))),
        _ => unreachable!(),
    }
    .map_err(|e| e.to_string())?;
    let _ = crate::storage::remember(&t.address);
    manager.reporter().connected(invited);
    let (sender, receiver) = mpsc::sync_channel::<(String, Value)>(32);
    let mut pending: HashMap<String, Arc<AtomicBool>> = HashMap::new();
    let mut last_frame = Instant::now();
    idle.connected();
    let online = Instant::now();
    let result = (|| -> Attempt {
        while !stopped.load(Ordering::SeqCst) {
            while let Ok((id, value)) = receiver.try_recv() {
                if let Some(cancel) = pending.remove(&id)
                    && !cancel.load(Ordering::SeqCst)
                {
                    ws.send(Message::Text(value.to_string().into()))
                        .map_err(|e| e.to_string())?;
                }
            }
            if heartbeat && last_frame.elapsed() > Duration::from_secs(60) {
                return Err("server heartbeat timed out".to_string().into());
            }
            match ws.read() {
                Ok(message) => {
                    last_frame = Instant::now();
                    match message {
                        Message::Text(text) => {
                            let value: Value =
                                serde_json::from_str(&text).map_err(|e| e.to_string())?;
                            let id = value["id"].as_str().unwrap_or("").to_string();
                            match value["type"].as_str().unwrap_or("") {
                                "api_call" => {
                                    if pending.len() >= 32 || pending.contains_key(&id) {
                                        ws.send(Message::Text(json!({"type":"api_result","id":id,"error":"too many pending requests or duplicate request ID"}).to_string().into())).map_err(|e|e.to_string())?;
                                        continue;
                                    }
                                    let canceled = Arc::new(AtomicBool::new(false));
                                    pending.insert(id.clone(), canceled.clone());
                                    let manager = manager.clone();
                                    let sender = sender.clone();
                                    let request = value["request"].clone();
                                    std::thread::spawn(move || {
                                        let reply = match manager.dispatch(request, &canceled) {
                                            Ok(data) => {
                                                json!({"type":"api_result","id":id,"data":data})
                                            }
                                            Err(error) => {
                                                json!({"type":"api_result","id":id,"error":error})
                                            }
                                        };
                                        let _ = sender.send((id, reply));
                                    });
                                }
                                "latency" => {
                                    if let Some(ms) = value
                                        .get("latency_ms")
                                        .map_or(Some(0), serde_json::Value::as_u64)
                                    {
                                        manager.reporter().send(ui::Event::Latency(ms));
                                    }
                                }
                                "api_cancel" => {
                                    if let Some(c) = pending.get(&id) {
                                        c.store(true, Ordering::SeqCst);
                                    }
                                }
                                _ => {}
                            }
                        }
                        Message::Close(frame) => {
                            let code = frame.as_ref().map(|f| u16::from(f.code)).unwrap_or(1000);
                            let _ = ws.flush();
                            if code == 1000 || code == 1001 {
                                return Ok(());
                            }
                            return Err(Failure {
                                message: format!("server closed connection ({code})"),
                                permanent: [1002, 1003, 1008].contains(&code),
                                retry_after: 0,
                                online: Duration::ZERO,
                            });
                        }
                        _ => {
                            ws.flush().map_err(|e| e.to_string())?;
                        }
                    }
                }
                Err(tungstenite::Error::Io(e))
                    if matches!(
                        e.kind(),
                        io::ErrorKind::WouldBlock
                            | io::ErrorKind::TimedOut
                            | io::ErrorKind::Interrupted
                    ) => {}
                Err(e) => return Err(e.to_string().into()),
            }
        }
        Ok(())
    })();
    for canceled in pending.values() {
        canceled.store(true, Ordering::SeqCst);
    }
    let _ = ws.close(None);
    result.map_err(|mut error: Failure| {
        error.online = online.elapsed();
        error
    })
}

fn duration_text(duration: Duration) -> String {
    let seconds = duration.as_secs() + u64::from(duration.subsec_millis() >= 500);
    if seconds >= 3600 {
        format!(
            "{}h{}m{}s",
            seconds / 3600,
            seconds % 3600 / 60,
            seconds % 60
        )
    } else if seconds >= 60 {
        format!("{}m{}s", seconds / 60, seconds % 60)
    } else {
        format!("{seconds}s")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn chunked() {
        assert_eq!(unchunk(b"2\r\n{}\r\n0\r\n\r\n").unwrap(), b"{}");
        assert!(unchunk(b"ffff\r\nx").is_err());
    }

    #[test]
    fn idle_timeout_starts_at_first_connection_and_survives_reconnects() {
        let stopped = Arc::new(AtomicBool::new(false));
        let manager = Manager::new(stopped.clone());
        let idle = IdleTracker::new(&manager, &stopped, Some(Duration::from_millis(40)));
        std::thread::sleep(Duration::from_millis(80));
        assert!(!stopped.load(Ordering::SeqCst));
        idle.connected();
        assert!(!stopped.load(Ordering::SeqCst));
        let deadline = Instant::now() + Duration::from_millis(400);
        // Frequent reconnections must not extend the original idle deadline.
        while !stopped.load(Ordering::SeqCst) && Instant::now() < deadline {
            idle.connected();
            std::thread::sleep(Duration::from_millis(5));
        }
        assert!(stopped.load(Ordering::SeqCst));
        drop(idle);
        manager.close();
    }

    #[test]
    fn dropping_idle_tracker_cancels_its_timer() {
        let stopped = Arc::new(AtomicBool::new(false));
        let manager = Manager::new(stopped.clone());
        let idle = IdleTracker::new(&manager, &stopped, Some(Duration::from_millis(40)));
        idle.connected();
        drop(idle);
        std::thread::sleep(Duration::from_millis(80));
        assert!(!stopped.load(Ordering::SeqCst));
        manager.close();
    }
}
