//! Each connection owns its sink. Late shell output can never reach a newer UI.
use super::{finished, line, output, started, text};
use crate::protocol::Token;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc::{SyncSender, TrySendError},
};
use std::time::Duration;

#[derive(Debug)]
pub enum Event {
    Connecting,
    Connected,
    Redeemed(Token),
    Retry {
        message: String,
        delay: u64,
    },
    Latency(u64),
    Started {
        id: String,
        command: String,
        time: String,
    },
    Output {
        id: String,
        data: String,
    },
    Finished {
        id: String,
        code: i32,
    },
    Message(String),
}

#[derive(Clone, Default)]
pub struct Reporter {
    sink: Option<(SyncSender<Event>, Arc<AtomicBool>)>,
}

impl Reporter {
    pub fn channel(sender: SyncSender<Event>, stopped: Arc<AtomicBool>) -> Self {
        Self {
            sink: Some((sender, stopped)),
        }
    }

    // Bounded, cancellation-aware backpressure: fast output cannot grow memory
    // without limit or strand a shell reader when the UI disconnects/exits.
    pub fn send(&self, mut event: Event) {
        if let Some((sender, stopped)) = &self.sink {
            loop {
                match sender.try_send(event) {
                    Ok(()) | Err(TrySendError::Disconnected(_)) => return,
                    Err(TrySendError::Full(value)) => {
                        if stopped.load(Ordering::SeqCst) {
                            return;
                        }
                        event = value;
                    }
                }
                std::thread::sleep(Duration::from_millis(5));
            }
        }
    }

    pub fn message(&self, en: &str, cn: &str) {
        if self.sink.is_some() {
            self.send(Event::Message(text(en, cn).into()));
        } else {
            line(en, cn);
        }
    }
    pub fn connecting(&self) {
        self.send(Event::Connecting);
    }
    pub fn connected(&self, invited: bool) {
        if self.sink.is_some() {
            self.send(Event::Connected);
        } else if invited {
            line(
                "[4/4] Connected. Close the client to disconnect.",
                "[4/4] 连接成功。关闭客户端即可断开连接。",
            );
        } else {
            line(
                "[Agenterm] Connected. Commands from the server will appear here.",
                "[Agenterm] 已连接。服务器的命令将显示在这里。",
            );
        }
    }
    pub fn retry(&self, message: String, delay: u64) {
        if self.sink.is_some() {
            self.send(Event::Retry { message, delay });
        } else {
            println!("\n[Agenterm] {message}");
        }
    }
    pub fn started(&self, id: &str, command: &str) {
        if self.sink.is_some() {
            self.send(Event::Started {
                id: id.into(),
                command: command.into(),
                time: timestamp(),
            });
        } else {
            started(command);
        }
    }
    pub fn output(&self, id: &str, data: &[u8]) {
        if self.sink.is_some() {
            if !id.is_empty() {
                self.send(Event::Output {
                    id: id.into(),
                    data: String::from_utf8_lossy(data).into_owned(),
                });
            }
        } else {
            output(data);
        }
    }
    pub fn finished(&self, id: &str, code: i32) {
        if self.sink.is_some() {
            self.send(Event::Finished {
                id: id.into(),
                code,
            });
        } else {
            finished(code);
        }
    }
}

pub fn timestamp() -> String {
    #[cfg(unix)]
    {
        let mut local: libc::tm = unsafe { std::mem::zeroed() };
        let now = unsafe { libc::time(std::ptr::null_mut()) };
        if !unsafe { libc::localtime_r(&now, &mut local) }.is_null() {
            return format!(
                "{:02}:{:02}:{:02}",
                local.tm_hour, local.tm_min, local.tm_sec
            );
        }
    }
    #[cfg(windows)]
    {
        let mut local = unsafe { std::mem::zeroed() };
        unsafe { windows_sys::Win32::System::SystemInformation::GetLocalTime(&mut local) };
        return format!(
            "{:02}:{:02}:{:02}",
            local.wHour, local.wMinute, local.wSecond
        );
    }
    #[allow(unreachable_code)]
    "00:00:00".into()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn cancellation_releases_backpressure_without_losing_a_redeemed_credential() {
        let stopped = Arc::new(AtomicBool::new(false));
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        let reporter = Reporter::channel(tx, stopped.clone());
        reporter.send(Event::Connecting);
        let worker = std::thread::spawn(move || reporter.output("one", b"bounded output"));
        std::thread::sleep(Duration::from_millis(20));
        stopped.store(true, Ordering::SeqCst);
        let deadline = std::time::Instant::now() + Duration::from_secs(1);
        while !worker.is_finished() && std::time::Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(5));
        }
        assert!(worker.is_finished());
        worker.join().unwrap();
        drop(rx);
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        let reporter = Reporter::channel(tx, stopped);
        reporter.send(Event::Message(
            "redemption completed during cancellation".into(),
        ));
        assert!(rx.try_recv().is_ok());
    }
}
