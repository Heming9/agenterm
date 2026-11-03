use crate::{
    signaling::Config,
    transfer_file::{Incoming, Verification, hex},
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::VecDeque,
    error::Error,
    fs::File,
    io::Read,
    net::{IpAddr, SocketAddr, ToSocketAddrs, UdpSocket},
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};
const CHUNK: usize = 16 * 1024;
const WINDOW: u64 = 1024 * 1024;
const ACK_INTERVAL: u64 = 128 * 1024;

pub(crate) struct Socket {
    pub(crate) udp: UdpSocket,
    pub(crate) base: SocketAddr,
    pub(crate) mapped: Option<SocketAddr>,
}

pub(crate) fn gather(
    config: &Config,
    canceled: &AtomicBool,
) -> Result<Vec<Socket>, Box<dyn Error>> {
    let mut ips = Vec::new();
    if let Some(ip) = config.bind_ip {
        ips.push(ip);
    } else {
        let route = UdpSocket::bind("0.0.0.0:0")?;
        route.connect("1.1.1.1:53")?;
        if let IpAddr::V4(ip) = route.local_addr()?.ip() {
            ips.push(ip);
        }
        for interface in if_addrs::get_if_addrs()? {
            if let IpAddr::V4(ip) = interface.ip()
                && !ip.is_loopback()
                && !ip.is_unspecified()
                && !ips.contains(&ip)
            {
                ips.push(ip);
            }
        }
    }
    let stun = if config.stun == "none" {
        None
    } else {
        Some(
            config
                .stun
                .to_socket_addrs()?
                .find(|a| a.is_ipv4())
                .ok_or("STUN has no IPv4 address")?,
        )
    };
    let mut sockets = Vec::new();
    for ip in ips.into_iter().take(8) {
        if config.canceled(canceled) {
            return Err("canceled".into());
        }
        let udp = match UdpSocket::bind((ip, 0)) {
            Ok(s) => s,
            Err(_) => continue,
        };
        let base = udp.local_addr()?;
        sockets.push(Socket {
            udp,
            base,
            mapped: None,
        });
    }
    let deadline = Instant::now() + Duration::from_secs(5.min(config.timeout_seconds));
    if let Some(server) = stun {
        std::thread::scope(|scope| {
            let jobs: Vec<_> = sockets
                .iter_mut()
                .map(|socket| {
                    scope.spawn(move || {
                        socket.mapped = super::stun_address_until(&socket.udp, server, || {
                            config.canceled(canceled) || Instant::now() >= deadline
                        })
                        .ok();
                    })
                })
                .collect();
            for job in jobs {
                let _ = job.join();
            }
        });
    }
    for socket in &sockets {
        socket.udp.set_nonblocking(true)?;
        eprintln!(
            "{}",
            json!({"event":"candidate","host":socket.base.to_string(),"srflx":socket.mapped.map(|a|a.to_string())})
        );
    }
    if sockets.is_empty() {
        return Err("no usable IPv4 interfaces".into());
    }
    Ok(sockets)
}

#[derive(PartialEq)]
enum Phase {
    Waiting,
    MetaSent,
    Receiving,
    Sending,
    FinishSent,
    Verifying,
    CompleteSent,
    DoneSent,
    Success,
    Failed,
}

pub(crate) struct Transfer {
    sender: bool,
    config: Config,
    canceled: Arc<AtomicBool>,
    path: PathBuf,
    file: Option<File>,
    incoming: Option<Incoming>,
    verification: Option<Verification>,
    phase: Phase,
    queue: VecDeque<(bool, Vec<u8>)>,
    size: u64,
    bytes: u64,
    acknowledged: u64,
    hash: Sha256,
    digest: String,
    committed: bool,
    overwrite: bool,
    disk_committed: Option<Arc<AtomicBool>>,
}

impl Transfer {
    pub(crate) fn new_copy(
        sender: bool,
        path: PathBuf,
        config: Config,
        canceled: Arc<AtomicBool>,
        overwrite: bool,
    ) -> Result<Self, Box<dyn Error>> {
        let file = if sender {
            if !std::fs::metadata(&path)?.is_file() {
                return Err("source must be a regular file".into());
            }
            let mut options = std::fs::OpenOptions::new();
            options.read(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                // A source can change after metadata(); never block the RTC
                // loop opening a FIFO, and recheck the opened handle below.
                options.custom_flags(libc::O_NONBLOCK);
            }
            Some(options.open(&path)?)
        } else {
            None
        };
        let size = if let Some(f) = &file {
            let metadata = f.metadata()?;
            if !metadata.is_file() {
                return Err("source must be a regular file".into());
            }
            if metadata.len() > config.max_bytes {
                return Err("source exceeds configured limit".into());
            }
            metadata.len()
        } else {
            if let Ok(meta) = std::fs::symlink_metadata(&path)
                && !(overwrite && meta.file_type().is_file())
            {
                return Err("destination exists".into());
            }
            0
        };
        Ok(Self {
            sender,
            config,
            canceled,
            path,
            file,
            incoming: None,
            verification: None,
            phase: Phase::Waiting,
            queue: VecDeque::new(),
            size,
            bytes: 0,
            acknowledged: 0,
            hash: Sha256::new(),
            digest: String::new(),
            committed: false,
            overwrite,
            disk_committed: None,
        })
    }
    fn control(&mut self, value: Value) -> Result<(), Box<dyn Error>> {
        if self.queue.len() >= 64 {
            return Err("control queue limit exceeded".into());
        }
        self.queue.push_back((false, serde_json::to_vec(&value)?));
        Ok(())
    }
    fn prepare(&mut self) -> Result<(), Box<dyn Error>> {
        if self.phase != Phase::Sending || !self.queue.is_empty() {
            return Ok(());
        }
        if self.bytes == self.size {
            if self.file.as_ref().unwrap().metadata()?.len() != self.size {
                return Err("source size changed during transfer".into());
            }
            self.digest = hex(&self.hash.clone().finalize());
            self.control(json!({"t":"finish","bytes":self.bytes,"sha256":self.digest}))?;
            self.phase = Phase::FinishSent;
        } else if self.bytes - self.acknowledged < WINDOW {
            let n = CHUNK.min((self.size - self.bytes) as usize);
            let mut data = vec![0; n];
            self.file.as_mut().unwrap().read_exact(&mut data)?;
            self.hash.update(&data);
            self.bytes += n as u64;
            self.queue.push_back((true, data));
        }
        Ok(())
    }
    fn receive(&mut self, binary: bool, data: &[u8]) -> Result<(), Box<dyn Error>> {
        if binary {
            if self.sender || self.phase != Phase::Receiving {
                return Err("file chunk in unexpected state".into());
            }
            let incoming = self.incoming.as_mut().ok_or("missing destination file")?;
            incoming.write(data)?;
            self.bytes = incoming.bytes;
            if self.bytes - self.acknowledged >= ACK_INTERVAL {
                self.acknowledged = self.bytes;
                self.control(json!({"t":"progress","bytes":self.bytes}))?;
            }
            return Ok(());
        }
        if data.len() > 4096 {
            return Err("oversized control message".into());
        }
        let message: Value = serde_json::from_slice(data)?;
        let kind = message["t"].as_str().ok_or("missing message type")?;
        match kind {
            "error" => {
                return Err(format!(
                    "peer failed: {}",
                    message["message"].as_str().unwrap_or("unknown error")
                )
                .into());
            }
            "meta" if !self.sender && self.phase == Phase::Waiting => {
                if message["v"] != 1 || message["session"] != self.config.session {
                    return Err("protocol or task mismatch".into());
                }
                self.size = message["size"].as_u64().ok_or("invalid file size")?;
                self.incoming = Some(Incoming::create_with_overwrite(
                    &self.path,
                    self.size,
                    self.config.max_bytes,
                    self.overwrite,
                )?);
                self.disk_committed = Some(self.incoming.as_ref().unwrap().committed.clone());
                self.phase = Phase::Receiving;
                self.control(json!({"t":"ready","session":self.config.session}))?;
            }
            "ready" if self.sender && self.phase == Phase::MetaSent => {
                if message["session"] != self.config.session {
                    return Err("task mismatch".into());
                }
                self.phase = Phase::Sending;
            }
            "progress"
                if self.sender && matches!(self.phase, Phase::Sending | Phase::FinishSent) =>
            {
                let bytes = message["bytes"].as_u64().ok_or("invalid progress")?;
                if bytes < self.acknowledged || bytes > self.bytes {
                    return Err("invalid progress offset".into());
                }
                self.acknowledged = bytes;
            }
            "finish" if !self.sender && self.phase == Phase::Receiving => {
                if message["bytes"].as_u64() != Some(self.size) || self.bytes != self.size {
                    return Err("incomplete transfer".into());
                }
                let hash = message["sha256"].as_str().ok_or("missing checksum")?;
                if hash.len() != 64 || !hash.bytes().all(|b| b.is_ascii_hexdigit()) {
                    return Err("invalid checksum".into());
                }
                self.digest = hash.into();
                self.verification = Some(Verification::start(
                    self.incoming.take().ok_or("missing file")?,
                    hash.into(),
                    self.canceled.clone(),
                ));
                self.phase = Phase::Verifying;
                eprintln!("{}", json!({"event":"verifying","bytes":self.bytes}));
            }
            "complete" if self.sender && self.phase == Phase::FinishSent => {
                if message["sha256"] != self.digest || message["bytes"].as_u64() != Some(self.size)
                {
                    return Err("receiver confirmation mismatch".into());
                }
                self.acknowledged = self.size;
                self.committed = true;
                self.phase = Phase::DoneSent;
                self.control(json!({"t":"done"}))?;
            }
            "done" if !self.sender && self.phase == Phase::CompleteSent => {
                self.control(json!({"t":"bye"}))?;
                self.phase = Phase::Success;
            }
            "bye" if self.sender && self.phase == Phase::DoneSent => {
                self.phase = Phase::Success;
            }
            _ => return Err(format!("unexpected control message: {kind}").into()),
        }
        Ok(())
    }
    fn poll_verification(&mut self) -> Result<(), Box<dyn Error>> {
        if let Some(result) = self.verification.as_mut().and_then(|v| v.result()) {
            self.digest = result?;
            self.committed = true;
            self.phase = Phase::CompleteSent;
            self.control(json!({"t":"complete","bytes":self.bytes,"sha256":self.digest}))?;
        }
        Ok(())
    }
}

impl Transfer {
    pub(crate) fn begin(&mut self) -> Result<(), Box<dyn Error>> {
        if self.sender {
            self.control(json!({"t":"meta","v":1,"session":self.config.session,"size":self.size}))?;
            self.phase = Phase::MetaSent;
        }
        Ok(())
    }
    pub(crate) fn frame(&mut self, binary: bool, data: &[u8]) -> Result<(), Box<dyn Error>> {
        self.receive(binary, data)
    }
    pub(crate) fn tick(&mut self) -> Result<(), Box<dyn Error>> {
        self.poll_verification()?;
        self.prepare()
    }
    pub(crate) fn take_frame(&mut self) -> Option<(bool, Vec<u8>)> {
        self.queue.pop_front()
    }
    pub(crate) fn return_frame(&mut self, frame: (bool, Vec<u8>)) {
        self.queue.push_front(frame);
    }
    pub(crate) fn finished(&self) -> bool {
        // The daemon keeps driving SCTP after a copy; a per-file exit grace
        // period would only race the next command on the same connection.
        self.phase == Phase::Success && self.queue.is_empty()
    }
    pub(crate) fn progress_bytes(&self) -> u64 {
        if self.sender {
            self.acknowledged
        } else {
            self.bytes
        }
    }
    pub(crate) fn snapshot(&self) -> Value {
        json!({"bytes":self.progress_bytes(),"total":self.size,"sha256":self.digest,"receiver_committed":self.committed || self.disk_committed.as_ref().is_some_and(|v|v.load(Ordering::Acquire))})
    }
    pub(crate) fn cancel(&mut self) {
        self.canceled.store(true, Ordering::Relaxed);
        if let Some(worker) = self.verification.as_mut()
            && let Some(Ok(hash)) = worker.wait()
        {
            self.digest = hash;
            self.committed = true;
        }
        drop(self.incoming.take());
        self.phase = Phase::Failed;
        self.queue.clear();
    }
}
