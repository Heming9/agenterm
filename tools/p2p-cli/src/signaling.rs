//! Bounded HTTPS signaling. Only SDP is sent through this service.
use serde::Deserialize;
use std::{
    io::Read,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

#[derive(Clone, Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub signal_url: String,
    pub session: String,
    pub token: String,
    #[serde(default = "default_stun")]
    pub stun: String,
    #[serde(default = "default_timeout")]
    pub timeout_seconds: u64,
    #[serde(default = "default_limit")]
    pub max_bytes: u64,
    pub cancel_file: Option<PathBuf>,
    pub bind_ip: Option<std::net::Ipv4Addr>,
    pub offerer: Option<bool>,
    pub client_id: Option<String>,
    #[serde(default = "register_by_default")]
    pub register: bool,
}
fn register_by_default() -> bool {
    true
}
fn default_stun() -> String {
    "stun.l.google.com:19302".into()
}
fn default_timeout() -> u64 {
    600
}
fn default_limit() -> u64 {
    10 * 1024 * 1024 * 1024
}

impl Config {
    pub fn load(path: &str) -> Result<Self, Box<dyn std::error::Error>> {
        let config: Self = if path == "-" {
            serde_json::from_reader(std::io::stdin().lock().take(16 * 1024))?
        } else {
            serde_json::from_reader(std::fs::File::open(path)?.take(16 * 1024))?
        };
        config.validate()?;
        Ok(config)
    }
    pub fn validate(&self) -> Result<(), Box<dyn std::error::Error>> {
        let config = self;
        let url = ureq::http::Uri::try_from(config.signal_url.as_str())?;
        let local = matches!(url.host(), Some("127.0.0.1" | "localhost" | "[::1]"));
        if url.scheme_str() != Some("https") && !(local && url.scheme_str() == Some("http")) {
            return Err("signaling requires HTTPS (HTTP is allowed only on loopback)".into());
        }
        if url.authority().is_some_and(|a| a.as_str().contains('@')) || url.query().is_some() {
            return Err("signaling URL must not contain credentials or query parameters".into());
        }
        if ![&config.token, &config.session]
            .iter()
            .all(|s| s.len() >= 32 && s.len() <= 128 && s.bytes().all(|b| b.is_ascii_hexdigit()))
        {
            return Err("session and token must each be 32..128 hex characters".into());
        }
        if !(1..=3600).contains(&config.timeout_seconds) {
            return Err("timeout must be 1..3600 seconds".into());
        }
        if config.client_id.as_ref().is_some_and(|id| {
            id.is_empty()
                || id.len() > 64
                || !id
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
        }) {
            return Err("invalid client ID".into());
        }
        Ok(())
    }
    pub fn canceled(&self, flag: &AtomicBool) -> bool {
        flag.load(Ordering::Relaxed) || self.cancel_file.as_ref().is_some_and(|p| p.exists())
    }
}

pub(crate) fn agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(4)))
        .max_redirects(0)
        .http_status_as_error(false)
        .build()
        .into()
}

pub fn register(config: &mut Config) -> Result<(), Box<dyn std::error::Error>> {
    if !config.register {
        return Ok(());
    }
    let url = format!(
        "{}/v1/{}/register",
        config.signal_url.trim_end_matches('/'),
        config.session
    );
    let client = agent();
    let deadline = Instant::now() + Duration::from_secs(config.timeout_seconds.min(12));
    let mut response = loop {
        match client
            .post(&url)
            .header("Authorization", format!("Bearer {}", config.token))
            .header("Content-Type", "application/json")
            .send(b"{}")
        {
            Ok(response) if response.status() == 200 => break response,
            Ok(response) if response.status().as_u16() < 500 => {
                return Err(format!("registration rejected: {}", response.status()).into());
            }
            _ => {}
        }
        if Instant::now() >= deadline {
            return Err("registration timed out".into());
        }
        std::thread::sleep(Duration::from_millis(200));
    };
    let data: serde_json::Value = serde_json::from_str(
        &response
            .body_mut()
            .with_config()
            .limit(16384)
            .read_to_string()?,
    )?;
    config.offerer = Some(
        data["offerer"]
            .as_bool()
            .ok_or("registration missing offerer")?,
    );
    config.client_id = Some(
        data["client_id"]
            .as_str()
            .ok_or("registration missing client ID")?
            .into(),
    );
    config.stun = data["stun"]
        .as_str()
        .ok_or("registration missing STUN configuration")?
        .into();
    config.validate()
}

pub fn publish(config: &Config, kind: &str, sdp: &str, stop: &AtomicBool) -> Result<(), String> {
    let body = serde_json::to_vec(&serde_json::json!({"type":kind,"sdp":sdp}))
        .map_err(|e| e.to_string())?;
    let url = format!(
        "{}/v1/{}/{}",
        config.signal_url.trim_end_matches('/'),
        config.session,
        kind
    );
    let deadline = Instant::now() + Duration::from_secs(config.timeout_seconds);
    let client = agent();
    loop {
        if config.canceled(stop) {
            return Err("canceled".into());
        }
        match client
            .put(&url)
            .header("Authorization", format!("Bearer {}", config.token))
            .header("Content-Type", "application/json")
            .send(&body)
        {
            Ok(response) if response.status() == 200 || response.status() == 201 => return Ok(()),
            Ok(response) if response.status().as_u16() < 500 => {
                return Err(format!("signaling publish rejected: {}", response.status()));
            }
            _ => {}
        }
        if Instant::now() >= deadline {
            return Err("signaling publish timed out".into());
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

pub fn fetch(config: &Config, kind: &str, stop: &AtomicBool) -> Result<String, String> {
    let url = format!(
        "{}/v1/{}/{}",
        config.signal_url.trim_end_matches('/'),
        config.session,
        kind
    );
    let deadline = Instant::now() + Duration::from_secs(config.timeout_seconds);
    let client = agent();
    loop {
        if config.canceled(stop) {
            return Err("canceled".into());
        }
        match client
            .get(&url)
            .header("Authorization", format!("Bearer {}", config.token))
            .call()
        {
            Ok(mut response) if response.status() == 200 => {
                let body = response
                    .body_mut()
                    .with_config()
                    .limit(128 * 1024)
                    .read_to_string()
                    .map_err(|_| "invalid or oversized signaling response")?;
                let value: serde_json::Value =
                    serde_json::from_str(&body).map_err(|_| "invalid signaling JSON")?;
                if value["type"] != kind {
                    return Err("unexpected signaling type".into());
                }
                return value["sdp"]
                    .as_str()
                    .map(String::from)
                    .ok_or("missing SDP".into());
            }
            Ok(response) if response.status() != 204 && response.status().as_u16() < 500 => {
                return Err(format!("signaling fetch rejected: {}", response.status()));
            }
            _ => {}
        }
        if Instant::now() >= deadline {
            return Err("signaling fetch timed out".into());
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

// Keep worker lifetime bounded and join on every exit, including cancellation.
pub struct Worker {
    handle: Option<std::thread::JoinHandle<Result<String, String>>>,
    stop: Arc<AtomicBool>,
}
impl Worker {
    pub fn start(config: Config, own: Option<String>, stop: Arc<AtomicBool>) -> Self {
        let flag = stop.clone();
        let handle = std::thread::spawn(move || {
            if let Some(sdp) = own {
                publish(&config, "offer", &sdp, &flag)?;
                fetch(&config, "answer", &flag)
            } else {
                fetch(&config, "offer", &flag)
            }
        });
        Self {
            handle: Some(handle),
            stop,
        }
    }
    pub fn answer(config: Config, sdp: String, stop: Arc<AtomicBool>) -> Self {
        let flag = stop.clone();
        Self {
            handle: Some(std::thread::spawn(move || {
                publish(&config, "answer", &sdp, &flag).map(|_| String::new())
            })),
            stop,
        }
    }
    pub fn result(&mut self) -> Option<Result<String, String>> {
        if !self.handle.as_ref()?.is_finished() {
            return None;
        }
        Some(
            self.handle
                .take()
                .unwrap()
                .join()
                .unwrap_or_else(|_| Err("signaling worker panicked".into())),
        )
    }
}
impl Drop for Worker {
    fn drop(&mut self) {
        if let Some(handle) = self.handle.take() {
            self.stop.store(true, Ordering::Relaxed);
            let _ = handle.join();
        }
    }
}
