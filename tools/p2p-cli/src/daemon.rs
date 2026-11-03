//! One owner-only local command endpoint drives one persistent P2P connection.
use crate::{
    signaling::{self, Config, Worker},
    transfer::{self, Socket, Transfer},
    transfer_file::hex,
};
use fs2::FileExt;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
#[cfg(unix)]
use std::process::{Command, Stdio};
use std::{
    collections::VecDeque,
    error::Error,
    fs::{self, File, OpenOptions},
    io::{self, BufRead, BufReader, Read, Write},
    net::{SocketAddr, TcpListener, TcpStream},
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    time::{Duration, Instant},
};
use str0m::{
    Candidate, Event, Input, Output, Rtc,
    change::{SdpAnswer, SdpOffer},
    channel::ChannelId,
    crypto::from_feature_flags,
    net::{Protocol, Receive},
};

type Result<T> = std::result::Result<T, Box<dyn Error>>;
const CONTROL_LIMIT: u64 = 64 * 1024;

#[derive(Clone, Deserialize, Serialize)]
struct LocalEndpoint {
    port: u16,
    token: String,
    pid: u32,
    session: String,
}

fn nonce() -> Result<String> {
    let mut bytes = [0; 16];
    getrandom::fill(&mut bytes)?;
    Ok(hex(&bytes))
}
fn state_dir(args: &[String]) -> Result<PathBuf> {
    if let Some(i) = args.iter().position(|s| s == "--state-dir") {
        return Ok(PathBuf::from(
            args.get(i + 1).ok_or("missing state directory")?,
        ));
    }
    if let Some(path) = std::env::var_os("P2P_CLI_STATE_DIR") {
        return Ok(PathBuf::from(path));
    }
    let home = std::env::var_os("HOME")
        .or_else(|| std::env::var_os("USERPROFILE"))
        .ok_or("cannot locate user home")?;
    Ok(PathBuf::from(home).join(".p2p-cli"))
}
fn private_dir(path: &Path) -> Result<()> {
    fs::create_dir_all(path)?;
    if fs::symlink_metadata(path)?.file_type().is_symlink() {
        return Err("state directory cannot be a symlink".into());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}
fn private_file(path: &Path) -> io::Result<File> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)
}
fn read_endpoint(dir: &Path) -> Result<LocalEndpoint> {
    Ok(serde_json::from_reader(
        File::open(dir.join("daemon.json"))?.take(4096),
    )?)
}
fn inactive_status(dir: &Path) -> Option<Value> {
    if let Ok(lock) = OpenOptions::new()
        .read(true)
        .write(true)
        .open(dir.join("daemon.lock"))
    {
        if lock.try_lock_exclusive().is_err() {
            return None;
        }
        let _ = fs::remove_file(dir.join("daemon.json"));
        let last = File::open(dir.join("last-status.json"))
            .ok()
            .and_then(|file| serde_json::from_reader(file.take(CONTROL_LIMIT)).ok());
        return Some(last.unwrap_or(json!({"ok":true,"done":true,"state":"stopped"})));
    }
    if read_endpoint(dir).is_err() {
        Some(json!({"ok":true,"done":true,"state":"stopped"}))
    } else {
        None
    }
}
fn authenticated_stream(dir: &Path) -> Result<(TcpStream, LocalEndpoint)> {
    let endpoint = read_endpoint(dir)?;
    let stream = TcpStream::connect_timeout(
        &SocketAddr::from(([127, 0, 0, 1], endpoint.port)),
        Duration::from_millis(500),
    )?;
    stream.set_write_timeout(Some(Duration::from_secs(5)))?;
    Ok((stream, endpoint))
}
fn wait_stopped(dir: &Path, session: &str) -> Result<()> {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        // A stop receipt precedes the daemon's final status write. Keep the
        // launcher alive until cleanup completes so its owner can safely reap
        // the process group immediately after this command returns.
        if !read_endpoint(dir).is_ok_and(|endpoint| endpoint.session == session) {
            return Ok(());
        }
        if inactive_status(dir).is_some() {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err("P2P stop acknowledged but local cleanup is still pending".into());
        }
        std::thread::sleep(Duration::from_millis(10));
    }
}
fn read_json_line(reader: &mut impl BufRead) -> Result<Value> {
    let mut data = Vec::new();
    reader
        .take(CONTROL_LIMIT + 1)
        .read_until(b'\n', &mut data)?;
    if data.len() as u64 > CONTROL_LIMIT || data.last() != Some(&b'\n') {
        return Err("invalid local command frame".into());
    }
    Ok(serde_json::from_slice(&data)?)
}
fn write_json(writer: &mut impl Write, value: &Value) -> io::Result<()> {
    serde_json::to_writer(&mut *writer, value)?;
    writer.write_all(b"\n")?;
    writer.flush()
}
fn invoke(dir: &Path, mut request: Value, display: bool) -> Result<Value> {
    let (mut stream, endpoint) = authenticated_stream(dir)?;
    request["token"] = json!(endpoint.token);
    write_json(&mut stream, &request)?;
    stream.set_read_timeout(Some(Duration::from_secs(if request["op"] == "cp" {
        3700
    } else {
        10
    })))?;
    let mut reader = BufReader::new(stream);
    loop {
        let response = read_json_line(&mut reader)?;
        if response["done"] == true {
            if display {
                println!("{}", serde_json::to_string_pretty(&response)?);
            }
            if response["ok"] != true {
                return Err(response["error"]
                    .as_str()
                    .unwrap_or("command failed")
                    .to_string()
                    .into());
            }
            return Ok(response);
        }
        if display {
            eprintln!("{response}");
        }
    }
}

fn status_output(mut status: Value, verbose: bool) -> Value {
    // Preserve the daemon's IPC/start response for bootstrap scripts and
    // already-running connections; reduce only the public status view.
    if verbose {
        if let Some(connection) = status.get_mut("connection").and_then(Value::as_object_mut) {
            connection.remove("local");
        }
        return status;
    }
    let mut output = json!({
        "state": status["state"],
        "client": if status["client"].is_null() { Value::Null } else {
            json!({"id": status["client"]["id"], "hostname": status["client"]["hostname"]})
        },
        "peer": if status["peer"].is_null() { Value::Null } else {
            json!({"id": status["peer"]["id"], "hostname": status["peer"]["hostname"],
                "state": status["connection"]["peer"]})
        },
        "copy": if status["copy"].is_null() { Value::Null } else {
            json!({"bytes": status["copy"]["bytes"], "total": status["copy"]["total"],
                "bytes_per_second": status["copy"]["bytes_per_second"]})
        }
    });
    if let Some(error) = status.get("error") {
        output["error"] = error.clone();
    }
    output
}

pub fn run() -> Result<()> {
    let args: Vec<String> = std::env::args().collect();
    let dir = state_dir(&args)?;
    match args.get(1).map(String::as_str) {
        Some("start") => {
            let i = args
                .iter()
                .position(|v| v == "--config")
                .ok_or("usage: p2p-cli start --config FILE|- [--foreground] [--state-dir DIR]")?;
            let config = Config::load(args.get(i + 1).ok_or("missing config")?)?;
            if let Ok(status) = invoke(&dir, json!({"op":"status"}), false) {
                if status["session"] == config.session {
                    println!("{}", serde_json::to_string_pretty(&status)?);
                    return Ok(());
                }
                return Err("a P2P connection is already running; stop it first".into());
            }
            private_dir(&dir)?;
            if args.iter().any(|v| v == "--foreground") {
                return serve(config, &dir);
            }
            let config_path = dir.join(format!("startup-{}.json", nonce()?));
            serde_json::to_writer(private_file(&config_path)?, &config)?;
            #[cfg(unix)]
            let mut child = {
                use std::os::unix::process::CommandExt;
                let log = OpenOptions::new()
                    .create(true)
                    .append(true)
                    .open(dir.join("daemon.log"))?;
                let mut command = Command::new(std::env::current_exe()?);
                command
                    .args(["_serve", "--config"])
                    .arg(&config_path)
                    .arg("--state-dir")
                    .arg(&dir)
                    .stdin(Stdio::null())
                    .stdout(Stdio::null())
                    .stderr(log);
                command.process_group(0);
                command.spawn()?
            };
            #[cfg(windows)]
            let mut child = crate::background_windows::spawn(&config_path, &dir)?;
            let deadline = Instant::now() + Duration::from_secs(30);
            loop {
                if let Some(code) = child.try_wait()? {
                    let _ = fs::remove_file(&config_path);
                    return Err(format!(
                        "connection process exited ({code}); see {}",
                        dir.join("daemon.log").display()
                    )
                    .into());
                }
                if let Ok(status) = invoke(&dir, json!({"op":"status"}), false)
                    && status["session"] == config.session
                    && status["state"] != "starting"
                {
                    println!("{}", serde_json::to_string_pretty(&status)?);
                    return Ok(());
                }
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    let _ = fs::remove_file(&config_path);
                    return Err("P2P initialization timed out".into());
                }
                std::thread::sleep(Duration::from_millis(50));
            }
        }
        Some("_serve") => {
            #[cfg(windows)]
            crate::background_windows::attach_log(&dir)?;
            let i = args
                .iter()
                .position(|v| v == "--config")
                .ok_or("missing config")?;
            let path = args.get(i + 1).ok_or("missing config")?;
            let config = Config::load(path)?;
            let path_ref = Path::new(path);
            if path_ref.parent().and_then(|p| fs::canonicalize(p).ok())
                != fs::canonicalize(&dir).ok()
                || !path_ref
                    .file_name()
                    .and_then(|n| n.to_str())
                    .is_some_and(|n| n.starts_with("startup-") && n.ends_with(".json"))
            {
                return Err("invalid internal startup file".into());
            }
            fs::remove_file(path)?;
            serve(config, &dir)
        }
        Some("status") => {
            let mut verbose = false;
            let mut i = 2;
            while i < args.len() {
                match args[i].as_str() {
                    "--verbose" | "-v" => {
                        verbose = true;
                        i += 1;
                    }
                    "--state-dir" => i += 2,
                    _ => {
                        return Err("usage: p2p-cli status [--verbose|-v] [--state-dir DIR]".into());
                    }
                }
            }
            let status = match invoke(&dir, json!({"op":"status"}), false) {
                Ok(status) => status,
                Err(error) => inactive_status(&dir).ok_or(error)?,
            };
            println!(
                "{}",
                serde_json::to_string_pretty(&status_output(status, verbose))?
            );
            Ok(())
        }
        Some("stop") => match invoke(
            &dir,
            json!({"op":"stop","session":args.iter().position(|v|v=="--session").and_then(|i|args.get(i+1))}),
            false,
        ) {
            Ok(response) => {
                if response["already_stopped"] != true
                    && let Some(session) = response["session"].as_str()
                {
                    wait_stopped(&dir, session)?;
                }
                println!("{}", serde_json::to_string_pretty(&response)?);
                Ok(())
            }
            Err(_) if inactive_status(&dir).is_some() => {
                println!(
                    "{}",
                    json!({"ok":true,"done":true,"state":"stopped","already_stopped":true})
                );
                Ok(())
            }
            Err(e) => Err(e),
        },
        Some("cp") => {
            let mut paths = Vec::new();
            let mut i = 2;
            while i < args.len() {
                match args[i].as_str() {
                    "--state-dir" => i += 2,
                    "--overwrite" => i += 1,
                    v => {
                        paths.push(v.to_string());
                        i += 1;
                    }
                }
            }
            if paths.len() != 2 {
                return Err(
                    "usage: p2p-cli cp CLIENT:PATH CLIENT:PATH [--overwrite] [--state-dir DIR]"
                        .into(),
                );
            }
            invoke(&dir,json!({"op":"cp","source":paths[0],"destination":paths[1],"overwrite":args.iter().any(|v|v=="--overwrite")}),true).map(|_|())
        }
        _ => Err("usage: p2p-cli start|status|cp|stop".into()),
    }
}

struct Cleanup(PathBuf);
impl Drop for Cleanup {
    fn drop(&mut self) {
        let _ = fs::remove_file(self.0.join("daemon.json"));
    }
}
struct LocalCommand {
    request: Value,
    reply: mpsc::SyncSender<Value>,
    id: String,
}
struct HandlerGuard(Arc<std::sync::atomic::AtomicUsize>);
impl Drop for HandlerGuard {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::Relaxed);
    }
}
fn listener(
    listener: TcpListener,
    token: String,
    commands: mpsc::SyncSender<LocalCommand>,
    stop: Arc<AtomicBool>,
) {
    let handlers = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    std::thread::spawn(move || {
        while !stop.load(Ordering::Relaxed) {
            let (mut stream, _) = match listener.accept() {
                Ok(pair) => pair,
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                    std::thread::sleep(Duration::from_millis(10));
                    continue;
                }
                Err(_) => break,
            };
            // Accepted sockets can inherit O_NONBLOCK on macOS.
            if stream.set_nonblocking(false).is_err() {
                continue;
            }
            if handlers.fetch_add(1, Ordering::Relaxed) >= 16 {
                handlers.fetch_sub(1, Ordering::Relaxed);
                continue;
            }
            let guard = HandlerGuard(handlers.clone());
            let token = token.clone();
            let commands = commands.clone();
            let stop = stop.clone();
            std::thread::spawn(move || {
                let _guard = guard;
                let _ = stream.set_read_timeout(Some(Duration::from_secs(3)));
                let _ = stream.set_write_timeout(Some(Duration::from_secs(3)));
                let request = match read_json_line(&mut BufReader::new(&mut stream)) {
                    Ok(v) => v,
                    Err(_) => return,
                };
                let provided = request["token"].as_str().unwrap_or("");
                if provided.len() != token.len()
                    || !provided
                        .bytes()
                        .zip(token.bytes())
                        .fold(0, |n, (a, b)| n | (a ^ b))
                        .eq(&0)
                {
                    let _ = write_json(
                        &mut stream,
                        &json!({"ok":false,"done":true,"error":"unauthorized"}),
                    );
                    return;
                }
                let (tx, rx) = mpsc::sync_channel(8);
                let id = match nonce() {
                    Ok(id) => id,
                    Err(_) => return,
                };
                if commands
                    .try_send(LocalCommand {
                        request,
                        reply: tx,
                        id: id.clone(),
                    })
                    .is_err()
                {
                    let _ = write_json(
                        &mut stream,
                        &json!({"ok":false,"done":true,"error":"command queue is full"}),
                    );
                    return;
                }
                loop {
                    match rx.recv_timeout(Duration::from_millis(100)) {
                        Ok(value) => {
                            if write_json(&mut stream, &value).is_err() {
                                let (tx, _) = mpsc::sync_channel(1);
                                let _ = commands.try_send(LocalCommand {
                                    request: json!({"op":"cancel_rpc"}),
                                    reply: tx,
                                    id,
                                });
                                break;
                            }
                            if value["done"] == true {
                                break;
                            }
                        }
                        Err(mpsc::RecvTimeoutError::Timeout) if !stop.load(Ordering::Relaxed) => {
                            continue;
                        }
                        _ => break,
                    }
                }
            });
        }
    });
}

fn serve(mut config: Config, dir: &Path) -> Result<()> {
    private_dir(dir)?;
    let lock = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(dir.join("daemon.lock"))?;
    lock.try_lock_exclusive()
        .map_err(|_| "a P2P connection is already running")?;
    let listener_socket = TcpListener::bind("127.0.0.1:0")?;
    listener_socket.set_nonblocking(true)?;
    let endpoint = LocalEndpoint {
        port: listener_socket.local_addr()?.port(),
        token: nonce()?,
        pid: std::process::id(),
        session: config.session.clone(),
    };
    let path = dir.join(format!("endpoint-{}.json", nonce()?));
    serde_json::to_writer(private_file(&path)?, &endpoint)?;
    #[cfg(windows)]
    {
        let _ = fs::remove_file(dir.join("daemon.json"));
    }
    fs::rename(path, dir.join("daemon.json"))?;
    let _ = fs::remove_file(dir.join("last-status.json"));
    let _cleanup = Cleanup(dir.to_path_buf());
    let stopped = Arc::new(AtomicBool::new(false));
    let signal = stopped.clone();
    ctrlc::set_handler(move || signal.store(true, Ordering::Relaxed))?;
    let (tx, rx) = mpsc::sync_channel(16);
    listener(listener_socket, endpoint.token, tx, stopped.clone());
    let mut final_status = json!({"ok":true,"done":true,"state":"stopped"});
    let result = (|| {
        signaling::register(&mut config)?;
        let client_id = config.client_id.clone().unwrap_or(nonce()?);
        let client = json!({"id":client_id,"hostname":hostname::get()?.to_string_lossy(),"platform":std::env::consts::OS,"arch":std::env::consts::ARCH,"cwd":std::env::current_dir()?.to_string_lossy()});
        let mut connection = Connection::new(config, client, rx, stopped.clone());
        let result = connection.drive();
        final_status = connection.status();
        final_status["state"] = json!(if result.is_ok() { "stopped" } else { "failed" });
        result
    })();
    stopped.store(true, Ordering::Relaxed);
    if let Err(e) = &result {
        final_status["state"] = json!("failed");
        final_status["error"] = json!(e.to_string());
        eprintln!(
            "{}",
            json!({"event":"connection_failed","error":e.to_string()})
        );
    }
    final_status["connection"]["local"] = final_status["state"].clone();
    if result.is_err() {
        final_status["connection"]["peer"] = json!("unknown");
    }
    let snapshot = dir.join(format!("last-{}.json", nonce()?));
    serde_json::to_writer(private_file(&snapshot)?, &final_status)?;
    #[cfg(windows)]
    {
        let _ = fs::remove_file(dir.join("last-status.json"));
    }
    fs::rename(snapshot, dir.join("last-status.json"))?;
    result
}

const RATE_WINDOW: Duration = Duration::from_secs(3);
const RATE_SAMPLE_INTERVAL: Duration = Duration::from_millis(100);

struct CopyRate {
    samples: VecDeque<(Instant, u64)>,
}
impl CopyRate {
    fn new(started: Instant) -> Self {
        Self {
            samples: VecDeque::from([(started, 0)]),
        }
    }
    fn observe(&mut self, now: Instant, bytes: u64) {
        let age = now.saturating_duration_since(self.samples.back().unwrap().0);
        if age < RATE_SAMPLE_INTERVAL {
            return;
        }
        // A suspended process has no samples for the missing interval. Start
        // a fresh window instead of attributing old progress to the present.
        if age > RATE_WINDOW {
            self.samples.clear();
        }
        self.samples.push_back((now, bytes));
        while self.samples.len() > 1
            && now.saturating_duration_since(self.samples[1].0) >= RATE_WINDOW
        {
            self.samples.pop_front();
        }
    }
    fn bytes_per_second(&self, now: Instant, bytes: u64) -> f64 {
        if now.saturating_duration_since(self.samples.back().unwrap().0) > RATE_WINDOW {
            return 0.0;
        }
        let (since, baseline) = self.samples.front().unwrap();
        let seconds = now.saturating_duration_since(*since).as_secs_f64();
        if seconds > 0.0 {
            bytes.saturating_sub(*baseline) as f64 / seconds
        } else {
            0.0
        }
    }
}

struct CopyJob {
    id: String,
    transfer: Transfer,
    reply: Option<mpsc::SyncSender<Value>>,
    rpc_id: String,
    started: Instant,
    progress: Instant,
    accepted: bool,
    rate: CopyRate,
}
impl CopyJob {
    fn snapshot(&self) -> Value {
        let mut snapshot = self.transfer.snapshot();
        let now = Instant::now();
        let bytes = self.transfer.progress_bytes();
        let seconds = now.saturating_duration_since(self.started).as_secs_f64();
        snapshot["bytes_per_second"] = json!(self.rate.bytes_per_second(now, bytes));
        snapshot["average_bytes_per_second"] = json!(if seconds > 0.0 {
            bytes as f64 / seconds
        } else {
            0.0
        });
        snapshot
    }
}
impl Drop for CopyJob {
    fn drop(&mut self) {
        self.transfer.cancel();
    }
}
struct Connection {
    config: Config,
    client: Value,
    peer: Option<Value>,
    peer_state: String,
    commands: mpsc::Receiver<LocalCommand>,
    shutdown: Arc<AtomicBool>,
    state: String,
    channel: Option<ChannelId>,
    queue: VecDeque<(bool, Vec<u8>)>,
    copy: Option<CopyJob>,
    last_peer: Instant,
    last_ping: Instant,
    closing: Option<Instant>,
    stop_reply: Option<mpsc::SyncSender<Value>>,
    peer_stopped: bool,
    created: Instant,
}
impl Connection {
    fn new(
        config: Config,
        client: Value,
        commands: mpsc::Receiver<LocalCommand>,
        shutdown: Arc<AtomicBool>,
    ) -> Self {
        Self {
            config,
            client,
            peer: None,
            peer_state: "unknown".into(),
            commands,
            shutdown,
            state: "negotiating".into(),
            channel: None,
            queue: VecDeque::new(),
            copy: None,
            last_peer: Instant::now(),
            last_ping: Instant::now(),
            closing: None,
            stop_reply: None,
            peer_stopped: false,
            created: Instant::now(),
        }
    }
    fn control(&mut self, message: Value) -> Result<()> {
        if self.queue.len() >= 64 {
            return Err("connection control queue exceeded".into());
        }
        self.queue.push_back((false, serde_json::to_vec(&message)?));
        Ok(())
    }
    fn status(&self) -> Value {
        json!({"ok":true,"done":true,"state":self.state,"session":self.config.session,"executable":std::env::current_exe().ok(),"client":self.client,"peer":self.peer,"connection":{"local":self.state,"peer":if self.last_peer.elapsed()<Duration::from_secs(10){self.peer_state.as_str()}else{"unknown"},"peer_observed_ms_ago":self.last_peer.elapsed().as_millis()},"copy":self.copy.as_ref().map(CopyJob::snapshot),"direct_only":true})
    }
    fn fail_copy(&mut self, message: &str, notify: bool) -> Result<()> {
        if let Some(mut copy) = self.copy.take() {
            copy.transfer.cancel();
            let mut result = copy.snapshot();
            result["ok"] = json!(false);
            result["done"] = json!(true);
            result["error"] = json!(message);
            if let Some(reply) = &copy.reply {
                let _ = reply.try_send(result);
            }
            if notify {
                self.control(json!({"t":"copy_error","id":copy.id,"message":message}))?;
            }
        }
        Ok(())
    }
    fn finish_copy(&mut self) {
        if self
            .copy
            .as_ref()
            .is_some_and(|copy| copy.transfer.finished())
            && let Some(copy) = self.copy.take()
            && let Some(reply) = &copy.reply
        {
            let mut result = copy.snapshot();
            result["ok"] = json!(true);
            result["done"] = json!(true);
            result["seconds"] = json!(copy.started.elapsed().as_secs_f64());
            let _ = reply.try_send(result);
        }
    }
    fn stop(&mut self, notify: bool) -> Result<()> {
        self.fail_copy("P2P connection stopped", notify)?;
        self.state = "stopping".into();
        self.closing = Some(Instant::now());
        if notify && self.channel.is_some() {
            self.control(json!({"t":"stop"}))?;
        }
        Ok(())
    }
    fn command(&mut self, command: LocalCommand) -> Result<()> {
        match command.request["op"].as_str() {
            Some("status") => {
                let _ = command.reply.try_send(self.status());
            }
            Some("stop") => {
                if let Some(session) = command.request["session"].as_str()
                    && session != self.config.session
                {
                    let _ = command.reply.try_send(
                        json!({"ok":true,"done":true,"already_stopped":true,"session":session}),
                    );
                    return Ok(());
                }
                self.stop_reply = Some(command.reply);
                self.stop(true)?;
            }
            Some("cancel_rpc") => {
                if self.copy.as_ref().is_some_and(|c| c.rpc_id == command.id) {
                    self.fail_copy("copy caller disconnected", true)?;
                }
            }
            Some("cp") => {
                self.finish_copy();
                let attempt = (|| -> Result<()> {
                    if self.state != "connected" {
                        return Err("P2P connection is not connected".into());
                    }
                    if self.copy.is_some() {
                        return Err("another copy is already running".into());
                    }
                    let (source_id, source) =
                        parse_path(command.request["source"].as_str().ok_or("missing source")?)?;
                    let (target_id, target) = parse_path(
                        command.request["destination"]
                            .as_str()
                            .ok_or("missing destination")?,
                    )?;
                    let local = self.client["id"].as_str().unwrap();
                    let peer = self.peer.as_ref().unwrap()["id"].as_str().unwrap();
                    if !((source_id == local && target_id == peer)
                        || (source_id == peer && target_id == local))
                    {
                        return Err(
                            "copy endpoints must be this client and its connected peer".into()
                        );
                    }
                    let overwrite = command.request["overwrite"].as_bool().unwrap_or(false);
                    let local_source = source_id == local;
                    let transfer = Transfer::new_copy(
                        local_source,
                        PathBuf::from(if local_source { source } else { target }),
                        self.config.clone(),
                        Arc::new(AtomicBool::new(false)),
                        overwrite,
                    )?;
                    let id = nonce()?;
                    self.control(json!({"t":"copy_request","id":id,"source_id":source_id,"target_id":target_id,"source":source,"target":target,"overwrite":overwrite}))?;
                    let started = Instant::now();
                    self.copy = Some(CopyJob {
                        id,
                        transfer,
                        reply: Some(command.reply.clone()),
                        rpc_id: command.id.clone(),
                        started,
                        progress: started,
                        accepted: false,
                        rate: CopyRate::new(started),
                    });
                    Ok(())
                })();
                if let Err(error) = attempt {
                    let _ = command
                        .reply
                        .try_send(json!({"ok":false,"done":true,"error":error.to_string()}));
                }
            }
            _ => {
                let _ = command
                    .reply
                    .try_send(json!({"ok":false,"done":true,"error":"unknown command"}));
            }
        }
        Ok(())
    }
    fn received(&mut self, binary: bool, data: &[u8]) -> Result<()> {
        self.last_peer = Instant::now();
        if binary {
            if data.len() < 33 || data.len() > 16 * 1024 + 33 {
                return Err("invalid copy frame".into());
            }
            if let Some(copy) = self.copy.as_mut()
                && copy.id.as_bytes() == &data[..32]
            {
                if data[32] > 1 {
                    return Err("invalid copy frame type".into());
                }
                if let Err(error) = copy.transfer.frame(data[32] == 1, &data[33..]) {
                    self.fail_copy(&error.to_string(), true)?;
                }
            }
            return Ok(());
        }
        if data.len() > 8192 {
            return Err("oversized connection message".into());
        }
        let message: Value = serde_json::from_slice(data)?;
        match message["t"].as_str() {
            Some("hello") => {
                if message["protocol"] != 1
                    || message["session"] != self.config.session
                    || self.peer.is_some()
                {
                    return Err("invalid peer handshake".into());
                }
                let id = message["client"]["id"].as_str().ok_or("peer ID missing")?;
                if id == self.client["id"].as_str().unwrap()
                    || id.is_empty()
                    || id.len() > 64
                    || !id
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
                {
                    return Err("invalid peer ID".into());
                }
                self.peer = Some(message["client"].clone());
                self.peer_state = "connected".into();
                self.state = "connected".into();
            }
            Some("ping") => {
                self.peer_state = message["state"].as_str().unwrap_or("unknown").into();
                self.control(json!({"t":"pong","state":self.state}))?;
            }
            Some("pong") => {
                self.peer_state = message["state"].as_str().unwrap_or("unknown").into();
            }
            Some("stop") => {
                self.peer_state = "stopping".into();
                self.stop(false)?;
                self.control(json!({"t":"stopped"}))?;
                self.peer_stopped = true;
            }
            Some("stopped") => {
                self.peer_state = "stopped".into();
                self.peer_stopped = true;
            }
            Some("copy_request") => {
                self.finish_copy();
                let id = message["id"].as_str().ok_or("copy ID missing")?.to_string();
                if id.len() != 32 || !id.bytes().all(|b| b.is_ascii_hexdigit()) {
                    return Err("invalid copy ID".into());
                }
                let prepare = (|| -> Result<Transfer> {
                    if self.state != "connected" || self.copy.is_some() {
                        return Err("peer is busy or not connected".into());
                    }
                    let local = self.client["id"].as_str().unwrap();
                    let peer = self.peer.as_ref().unwrap()["id"].as_str().unwrap();
                    let source_id = message["source_id"].as_str().ok_or("missing source ID")?;
                    let target_id = message["target_id"].as_str().ok_or("missing target ID")?;
                    if !((source_id == local && target_id == peer)
                        || (source_id == peer && target_id == local))
                    {
                        return Err("invalid copy endpoints".into());
                    }
                    let sender = source_id == local;
                    let path = message[if sender { "source" } else { "target" }]
                        .as_str()
                        .ok_or("missing path")?;
                    if path.is_empty() || path.contains('\0') {
                        return Err("invalid path".into());
                    }
                    Transfer::new_copy(
                        sender,
                        PathBuf::from(path),
                        self.config.clone(),
                        Arc::new(AtomicBool::new(false)),
                        message["overwrite"].as_bool().unwrap_or(false),
                    )
                })();
                match prepare {
                    Ok(mut transfer) => {
                        self.channel.ok_or("missing channel")?;
                        transfer.begin()?;
                        let started = Instant::now();
                        self.copy = Some(CopyJob {
                            id: id.clone(),
                            transfer,
                            reply: None,
                            rpc_id: String::new(),
                            started,
                            progress: started,
                            accepted: true,
                            rate: CopyRate::new(started),
                        });
                        self.control(json!({"t":"copy_accept","id":id}))?;
                    }
                    Err(error) => {
                        self.control(json!({"t":"copy_error","id":id,"message":error.to_string()}))?
                    }
                }
            }
            Some("copy_accept") => {
                if let Some(copy) = self.copy.as_mut()
                    && message["id"] == copy.id
                    && !copy.accepted
                {
                    self.channel.ok_or("missing channel")?;
                    copy.transfer.begin()?;
                    copy.accepted = true;
                }
            }
            Some("copy_error") => {
                if self.copy.as_ref().is_some_and(|c| message["id"] == c.id) {
                    self.fail_copy(
                        message["message"].as_str().unwrap_or("peer copy failed"),
                        false,
                    )?;
                }
            }
            _ => return Err("unknown connection message".into()),
        }
        Ok(())
    }
    fn drain(&mut self, rtc: &mut Rtc, sockets: &[Socket]) -> Result<Instant> {
        loop {
            match rtc.poll_output()? {
                Output::Timeout(t) => return Ok(t),
                Output::Transmit(packet) => {
                    let socket = sockets
                        .iter()
                        .find(|s| s.base == packet.source || s.mapped == Some(packet.source))
                        .ok_or("unknown source socket")?;
                    match socket.udp.send_to(&packet.contents, packet.destination) {
                        Ok(_) => {}
                        Err(e)
                            if matches!(
                                e.kind(),
                                io::ErrorKind::WouldBlock
                                    | io::ErrorKind::NetworkUnreachable
                                    | io::ErrorKind::HostUnreachable
                            ) => {}
                        Err(e) => return Err(e.into()),
                    }
                }
                Output::Event(Event::ChannelOpen(id, label)) => {
                    if label != "p2p-cli-v1" || self.channel.is_some() {
                        return Err("unexpected data channel".into());
                    }
                    self.channel = Some(id);
                    self.control(json!({"t":"hello","protocol":1,"session":self.config.session,"client":self.client}))?;
                }
                Output::Event(Event::ChannelData(data)) => {
                    if Some(data.id) != self.channel {
                        return Err("unknown channel".into());
                    }
                    self.received(data.binary, &data.data)?;
                }
                Output::Event(Event::ChannelClose(_)) if self.closing.is_none() => {
                    return Err("peer disconnected".into());
                }
                _ => {}
            }
        }
    }
    fn drive(&mut self) -> Result<()> {
        let result = self.drive_inner();
        self.fail_copy(
            if result.is_ok() {
                "connection stopped"
            } else {
                "connection failed"
            },
            false,
        )?;
        if let Some(reply) = self.stop_reply.take() {
            let _ = reply.try_send(
                json!({"ok":true,"done":true,"state":"stopped","session":self.config.session,"peer_confirmed":self.peer_stopped}),
            );
        }
        self.shutdown.store(true, Ordering::Relaxed);
        result
    }
    fn drive_inner(&mut self) -> Result<()> {
        let sockets = transfer::gather(&self.config, &self.shutdown)?;
        from_feature_flags().install_process_default();
        let mut rtc = Rtc::builder()
            .set_sctp_max_buffered_amount(2 * 1024 * 1024)
            .build(Instant::now());
        for socket in &sockets {
            rtc.add_local_candidate(Candidate::host(socket.base, "udp")?)
                .ok_or("candidate rejected")?;
            self.drain(&mut rtc, &sockets)?;
            if let Some(mapped) = socket.mapped {
                rtc.add_local_candidate(Candidate::server_reflexive(mapped, socket.base, "udp")?)
                    .ok_or("candidate rejected")?;
                self.drain(&mut rtc, &sockets)?;
            }
        }
        let offerer = self.config.offerer.unwrap_or(false);
        let mut pending = None;
        let own = if offerer {
            let mut changes = rtc.sdp_api();
            changes.add_channel("p2p-cli-v1".into());
            let (sdp, p) = changes.apply().ok_or("no offer")?;
            pending = Some(p);
            self.drain(&mut rtc, &sockets)?;
            Some(sdp.to_sdp_string())
        } else {
            None
        };
        let signaling_stop = Arc::new(AtomicBool::new(false));
        let mut worker = Worker::start(self.config.clone(), own, signaling_stop.clone());
        let mut applied = false;
        let mut buf = [0; 65536];
        loop {
            let timeout = self.drain(&mut rtc, &sockets)?;
            if let Some(copy) = self.copy.as_mut() {
                copy.rate
                    .observe(Instant::now(), copy.transfer.progress_bytes());
            }
            while let Ok(command) = self.commands.try_recv() {
                self.command(command)?;
            }
            if self.config.canceled(&self.shutdown) && self.closing.is_none() {
                self.stop(true)?;
            }
            if let Some(t) = self.closing {
                if self.channel.is_none()
                    || (self.peer_stopped
                        && self.queue.is_empty()
                        && t.elapsed() > Duration::from_millis(300))
                    || t.elapsed() > Duration::from_secs(3)
                {
                    return Ok(());
                }
            } else {
                if let Some(result) = worker.result() {
                    let sdp = result.map_err(io::Error::other)?;
                    if !applied {
                        let sdp = crate::normalize_candidates(&sdp)?;
                        rtc.handle_input(Input::Timeout(Instant::now()))?;
                        self.drain(&mut rtc, &sockets)?;
                        if offerer {
                            rtc.sdp_api().accept_answer(
                                pending.take().ok_or("no pending offer")?,
                                SdpAnswer::from_sdp_string(&sdp)?,
                            )?;
                        } else {
                            let answer = rtc
                                .sdp_api()
                                .accept_offer(SdpOffer::from_sdp_string(&sdp)?)?;
                            worker = Worker::answer(
                                self.config.clone(),
                                answer.to_sdp_string(),
                                signaling_stop.clone(),
                            );
                        }
                        applied = true;
                        continue;
                    }
                }
                if self.state != "connected"
                    && self.created.elapsed() > Duration::from_secs(self.config.timeout_seconds)
                {
                    return Err("connection deadline exceeded".into());
                }
                if self.peer.is_some() {
                    if self.last_peer.elapsed() > Duration::from_secs(20) {
                        return Err("peer keepalive timed out".into());
                    }
                    if self.last_ping.elapsed() > Duration::from_secs(3) {
                        self.control(json!({"t":"ping","state":self.state}))?;
                        self.last_ping = Instant::now();
                    }
                }
                if let Some(copy) = self.copy.as_mut() {
                    let error = if copy.started.elapsed()
                        > Duration::from_secs(self.config.timeout_seconds)
                    {
                        Some("copy deadline exceeded".to_string())
                    } else if copy.accepted {
                        copy.transfer.tick().err().map(|e| e.to_string())
                    } else {
                        None
                    };
                    if let Some(error) = error {
                        self.fail_copy(&error, true)?;
                    } else if copy.transfer.finished() {
                        self.finish_copy();
                    } else if copy.progress.elapsed() > Duration::from_secs(1) {
                        if let Some(reply) = &copy.reply {
                            let mut progress = copy.snapshot();
                            progress["done"] = json!(false);
                            let _ = reply.try_send(progress);
                        }
                        copy.progress = Instant::now();
                    }
                }
            }
            let mut input = false;
            for socket in &sockets {
                match socket.udp.recv_from(&mut buf) {
                    Ok((n, source)) => {
                        if let Ok(contents) = (&buf[..n]).try_into() {
                            rtc.handle_input(Input::Receive(
                                Instant::now(),
                                Receive {
                                    proto: Protocol::Udp,
                                    source,
                                    destination: socket.base,
                                    contents,
                                },
                            ))?;
                            input = true;
                            break;
                        }
                    }
                    Err(e) if e.kind() == io::ErrorKind::WouldBlock => {}
                    Err(e) => return Err(e.into()),
                }
            }
            if input {
                continue;
            }
            if timeout <= Instant::now() {
                rtc.handle_input(Input::Timeout(Instant::now()))?;
                continue;
            }
            let frame = self
                .queue
                .pop_front()
                .map(|frame| (frame, false))
                .or_else(|| {
                    let copy = self.copy.as_mut()?;
                    let (binary, data) = copy.transfer.take_frame()?;
                    let mut body = copy.id.as_bytes().to_vec();
                    body.push(u8::from(binary));
                    body.extend_from_slice(&data);
                    Some(((true, body), true))
                });
            if let Some(((binary, data), file)) = frame {
                let accepted = rtc
                    .channel(self.channel.ok_or("no channel")?)
                    .ok_or("channel closed")?
                    .write(binary, &data)?;
                if !accepted {
                    if file {
                        if let Some(copy) = self.copy.as_mut() {
                            copy.transfer
                                .return_frame((data[32] == 1, data[33..].to_vec()));
                        }
                    } else {
                        self.queue.push_front((binary, data));
                    }
                }
                if accepted {
                    continue;
                }
            }
            std::thread::sleep(
                timeout
                    .saturating_duration_since(Instant::now())
                    .min(Duration::from_millis(2)),
            );
        }
    }
}
fn parse_path(path: &str) -> Result<(&str, &str)> {
    let (client, path) = path
        .split_once(':')
        .ok_or("path must have CLIENT:PATH form")?;
    if client.is_empty() || path.is_empty() || path.contains('\0') {
        return Err("invalid client path".into());
    }
    Ok((client, path))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn copy_rate_handles_startup_and_empty_progress() {
        let started = Instant::now();
        let mut rate = CopyRate::new(started);
        assert_eq!(rate.bytes_per_second(started, 0), 0.0);
        assert_eq!(rate.bytes_per_second(started, 1), 0.0);
        assert_eq!(
            rate.bytes_per_second(started + Duration::from_secs(1), 0),
            0.0
        );
        rate.observe(started + Duration::from_secs(1), 1_000_000);
        assert_eq!(
            rate.bytes_per_second(started + Duration::from_secs(1), 1_000_000),
            1_000_000.0
        );
    }
    #[test]
    fn copy_rate_uses_recent_progress_and_decays_to_zero_when_stalled() {
        let started = Instant::now();
        let mut rate = CopyRate::new(started);
        let mut bytes = 0;
        for tick in 1..=90 {
            bytes += match tick {
                1..=30 => 100_000,
                31..=60 => 200_000,
                _ => 0,
            };
            let now = started + Duration::from_millis(tick * 100);
            rate.observe(now, bytes);
            assert!(rate.samples.len() <= 32);
            match tick {
                30 => assert_eq!(rate.bytes_per_second(now, bytes), 1_000_000.0),
                60 => assert_eq!(rate.bytes_per_second(now, bytes), 2_000_000.0),
                90 => assert_eq!(rate.bytes_per_second(now, bytes), 0.0),
                _ => {}
            }
        }
    }
    #[test]
    fn copy_rate_discards_stale_samples_and_resets_for_a_new_job() {
        let started = Instant::now();
        let mut rate = CopyRate::new(started);
        rate.observe(started + Duration::from_secs(1), 1_000_000);
        let resumed = started + Duration::from_secs(8);
        assert_eq!(rate.bytes_per_second(resumed, 1_000_000), 0.0);
        rate.observe(resumed, 1_000_000);
        assert_eq!(rate.bytes_per_second(resumed, 1_000_000), 0.0);
        let next = resumed + Duration::from_millis(100);
        rate.observe(next, 1_100_000);
        assert_eq!(rate.bytes_per_second(next, 1_100_000), 1_000_000.0);
        assert_eq!(CopyRate::new(next).bytes_per_second(next, 0), 0.0);
    }
    #[test]
    fn windows_paths_preserve_drive_and_colons() {
        assert_eq!(
            parse_path("A:C:\\Users\\a:b.txt").unwrap(),
            ("A", "C:\\Users\\a:b.txt")
        );
        assert!(parse_path("A:").is_err());
        assert!(parse_path("/tmp/a").is_err());
    }
}
