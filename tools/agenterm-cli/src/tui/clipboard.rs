use base64::{Engine, engine::general_purpose::STANDARD};
use std::{
    io::Write,
    process::{Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc::{self, Receiver, Sender},
    },
    time::{Duration, Instant},
};

pub struct Copied {
    pub selection: u64,
    pub request: u64,
    pub notice: &'static str,
    pub sequence: Option<String>,
}
pub struct Clipboard {
    serial: Arc<Mutex<()>>,
    latest: Arc<AtomicU64>,
    tx: Sender<Copied>,
    pub rx: Receiver<Copied>,
    stopped: Arc<AtomicBool>,
}
impl Default for Clipboard {
    fn default() -> Self {
        let (tx, rx) = mpsc::channel();
        Self {
            serial: Arc::default(),
            latest: Arc::default(),
            tx,
            rx,
            stopped: Arc::default(),
        }
    }
}
impl Drop for Clipboard {
    fn drop(&mut self) {
        self.stopped.store(true, Ordering::SeqCst);
    }
}
impl Clipboard {
    pub fn is_latest(&self, request: u64) -> bool {
        self.latest.load(Ordering::SeqCst) == request
    }
    pub fn copy(&self, text: String, selection: u64) -> u64 {
        let request = self.latest.fetch_add(1, Ordering::SeqCst) + 1;
        let serial = self.serial.clone();
        let latest = self.latest.clone();
        let tx = self.tx.clone();
        let stopped = self.stopped.clone();
        std::thread::spawn(move || {
            let _guard = serial.lock().unwrap();
            if latest.load(Ordering::SeqCst) != request || stopped.load(Ordering::SeqCst) {
                return;
            }
            let (notice, sequence) = write_clipboard(&text, &stopped);
            let _ = tx.send(Copied {
                selection,
                request,
                notice,
                sequence,
            });
        });
        request
    }
}
fn command() -> Option<Command> {
    if std::env::var_os("SSH_CONNECTION").is_some() || std::env::var_os("SSH_TTY").is_some() {
        return None;
    }
    if cfg!(target_os = "macos") {
        return Some(Command::new("pbcopy"));
    }
    if cfg!(windows) {
        let mut cmd = Command::new("powershell.exe");
        cmd.args(["-NoProfile","-NonInteractive","-Command","[Console]::InputEncoding = [System.Text.UTF8Encoding]::new(); Set-Clipboard -Value ([Console]::In.ReadToEnd())"]);
        return Some(cmd);
    }
    let candidates = [
        ("WAYLAND_DISPLAY", "wl-copy", vec![]),
        ("DISPLAY", "xclip", vec!["-selection", "clipboard"]),
        ("DISPLAY", "xsel", vec!["--clipboard", "--input"]),
    ];
    for (display, name, args) in candidates {
        if std::env::var_os(display).is_some()
            && std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default())
                .any(|p| p.join(name).is_file())
        {
            let mut cmd = Command::new(name);
            cmd.args(args);
            return Some(cmd);
        }
    }
    None
}
fn write_clipboard(text: &str, stopped: &AtomicBool) -> (&'static str, Option<String>) {
    if let Some(mut cmd) = command() {
        let result = (|| -> std::io::Result<bool> {
            let mut child = cmd
                .stdin(Stdio::piped())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()?;
            let mut input = child.stdin.take().unwrap();
            let text = text.to_owned();
            // Writing a large selection must not bypass the process timeout.
            let writer = std::thread::spawn(move || input.write_all(text.as_bytes()));
            let deadline = Instant::now() + Duration::from_secs(5);
            let success = loop {
                match child.try_wait() {
                    Ok(Some(status)) => break status.success(),
                    Ok(None) => {}
                    Err(_) => break false,
                }
                if stopped.load(Ordering::SeqCst) || Instant::now() >= deadline {
                    break false;
                }
                std::thread::sleep(Duration::from_millis(10));
            };
            let _ = child.kill();
            let _ = child.wait();
            Ok(success && writer.join().is_ok_and(|r| r.is_ok()))
        })();
        return (
            if result.unwrap_or(false) {
                "tui.selection.copied"
            } else {
                "tui.selection.copy.failed"
            },
            None,
        );
    }
    (
        "tui.selection.sent",
        Some(osc52(
            text,
            std::env::var_os("TMUX").is_some(),
            std::env::var("TERM")
                .unwrap_or_default()
                .starts_with("screen"),
        )),
    )
}
pub fn osc52(text: &str, tmux: bool, screen: bool) -> String {
    let encoded = STANDARD.encode(text);
    let payload = if screen && !tmux {
        encoded
            .as_bytes()
            .chunks(76)
            .map(|chunk| std::str::from_utf8(chunk).unwrap())
            .collect::<Vec<_>>()
            .join("\x1b\\\x1bP")
    } else {
        encoded
    };
    let sequence = format!("\x1b]52;c;{payload}\x07");
    if tmux {
        format!("\x1bPtmux;{}\x1b\\", sequence.replace('\x1b', "\x1b\x1b"))
    } else if screen {
        format!("\x1bP{sequence}\x1b\\")
    } else {
        sequence
    }
}
