use crate::ui::{Event, Reporter, reporter_timestamp};
use serde::Deserialize;
use std::{
    io::{self, Read},
    process::{Child, Command, ExitStatus, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread::{self, JoinHandle},
    time::{Duration, Instant},
};

pub const SUPPORTED: bool = cfg!(any(
    target_os = "macos",
    target_os = "linux",
    target_os = "windows"
)) && cfg!(any(target_arch = "aarch64", target_arch = "x86_64"));

const CONFIG: &str = "AGENTERM_P2P_CONFIG_B64";
const POLL: Duration = Duration::from_millis(25);
const STOP_TIMEOUT: Duration = Duration::from_secs(3);

#[derive(Deserialize)]
pub struct Launch {
    script: String,
    stop_script: String,
    #[serde(rename = "config_base64")]
    config_b64: String,
}

pub struct Bootstrap {
    canceled: Arc<AtomicBool>,
    worker: JoinHandle<()>,
}

impl Bootstrap {
    pub fn start(id: String, launch: Launch, reporter: Reporter) -> io::Result<Self> {
        if launch.script.is_empty()
            || launch.script.len() > 64 << 10
            || launch.stop_script.is_empty()
            || launch.stop_script.len() > 64 << 10
            || launch.config_b64.is_empty()
            || launch.config_b64.len() > 32 << 10
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid P2P bootstrap",
            ));
        }
        let canceled = Arc::new(AtomicBool::new(false));
        let worker_canceled = canceled.clone();
        let worker = thread::Builder::new().spawn(move || {
            reporter.p2p(Event::Started {
                id: id.clone(),
                command: "p2p-cli start".into(),
                time: reporter_timestamp(),
            });
            let code = match run(launch, &worker_canceled, &reporter, &id) {
                Ok(code) => code,
                Err(error) => {
                    reporter.p2p(Event::Output {
                        id: id.clone(),
                        data: format!("P2P bootstrap: {error}\n"),
                    });
                    -1
                }
            };
            reporter.p2p(Event::Finished { id, code });
        })?;
        Ok(Self { canceled, worker })
    }

    pub fn cancel(&self) {
        self.canceled.store(true, Ordering::SeqCst);
    }

    pub fn is_finished(&self) -> bool {
        self.worker.is_finished()
    }

    pub fn join(self) {
        let _ = self.worker.join();
    }
}

fn run(launch: Launch, canceled: &AtomicBool, reporter: &Reporter, id: &str) -> io::Result<i32> {
    if canceled.load(Ordering::SeqCst) {
        return Ok(-1);
    }
    let (mut reader, writer) = io::pipe()?;
    let mut process = Process::spawn(
        &launch.script,
        Some(&launch.config_b64),
        Stdio::from(writer.try_clone()?),
        Stdio::from(writer),
    )?;
    let result = (|| -> io::Result<bool> {
        while !canceled.load(Ordering::SeqCst) {
            drain_output(&mut reader, reporter, id);
            if !process.running()? {
                return Ok(true);
            }
            thread::sleep(POLL);
        }
        Ok(false)
    })();
    stop(&launch.stop_script);
    let status = process.finish();
    drain_output(&mut reader, reporter, id);
    if result? {
        Ok(status?.code().unwrap_or(-1))
    } else {
        Ok(-1)
    }
}

fn drain_output(reader: &mut io::PipeReader, reporter: &Reporter, id: &str) {
    // Limit progress per pass even when the helper continuously generates
    // output. Never wait for EOF, which a descendant could keep open.
    let mut buffer = [0; 4096];
    for _ in 0..4 {
        let ready = ready_bytes(reader).min(buffer.len());
        if ready == 0 {
            break;
        }
        match reader.read(&mut buffer[..ready]) {
            Ok(n) if n > 0 => reporter.p2p(Event::Output {
                id: id.into(),
                data: String::from_utf8_lossy(&buffer[..n]).into_owned(),
            }),
            _ => break,
        }
    }
}

#[cfg(unix)]
fn ready_bytes(reader: &io::PipeReader) -> usize {
    use std::os::fd::AsRawFd;
    let mut fd = libc::pollfd {
        fd: reader.as_raw_fd(),
        events: libc::POLLIN,
        revents: 0,
    };
    // There is one pipe reader, so POLLIN remains readable until this worker
    // consumes the data or sees EOF.
    if unsafe { libc::poll(&mut fd, 1, 0) } == 1 && fd.revents & libc::POLLIN != 0 {
        4096
    } else {
        0
    }
}

#[cfg(windows)]
fn ready_bytes(reader: &io::PipeReader) -> usize {
    use std::{os::windows::io::AsRawHandle, ptr::null_mut};
    use windows_sys::Win32::System::Pipes::PeekNamedPipe;
    let mut available = 0;
    if unsafe {
        PeekNamedPipe(
            reader.as_raw_handle(),
            null_mut(),
            0,
            null_mut(),
            &mut available,
            null_mut(),
        )
    } == 0
    {
        return 0;
    }
    available as usize
}

fn stop(script: &str) {
    let Ok(mut process) = Process::spawn(script, None, Stdio::null(), Stdio::null()) else {
        return;
    };
    let deadline = Instant::now() + STOP_TIMEOUT;
    while Instant::now() < deadline {
        if !matches!(process.running(), Ok(true)) {
            return;
        }
        thread::sleep(POLL);
    }
}

struct Process {
    child: Child,
    finished: bool,
    #[cfg(windows)]
    job: Option<crate::terminal::Job>,
}

impl Process {
    fn spawn(script: &str, config: Option<&str>, stdout: Stdio, stderr: Stdio) -> io::Result<Self> {
        #[cfg(unix)]
        let mut command = {
            use std::os::unix::process::CommandExt;
            let mut command = Command::new("/bin/sh");
            command
                .args(["-c", script])
                .stdin(Stdio::null())
                .process_group(0);
            command
        };
        #[cfg(windows)]
        let mut command = {
            use std::os::windows::process::CommandExt;
            let mut command = Command::new("powershell.exe");
            // Feed the script after assigning PowerShell to the Job. This
            // keeps native children contained even when cancellation races
            // with startup and avoids Windows command-line size limits.
            command
                .args([
                    "-NoLogo",
                    "-NoProfile",
                    "-NonInteractive",
                    "-Command",
                    "& ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))))",
                ])
                .stdin(Stdio::piped())
                .creation_flags(0x08000000); // CREATE_NO_WINDOW
            command
        };
        command.stdout(stdout).stderr(stderr).env_remove(CONFIG);
        if let Some(config) = config {
            command.env(CONFIG, config);
        }
        #[allow(unused_mut)]
        let mut child = command.spawn()?;
        #[cfg(windows)]
        {
            use base64::{Engine, engine::general_purpose::STANDARD};
            use std::io::Write;

            let job = match crate::terminal::Job::assign(&child) {
                Ok(job) => job,
                Err(error) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(io::Error::other(error));
                }
            };
            if let Some(mut stdin) = child.stdin.take() {
                let script = STANDARD.encode(script);
                thread::spawn(move || {
                    let _ = stdin.write_all(script.as_bytes());
                });
            }
            Ok(Self {
                child,
                finished: false,
                job: Some(job),
            })
        }
        #[cfg(unix)]
        Ok(Self {
            child,
            finished: false,
        })
    }

    fn running(&mut self) -> io::Result<bool> {
        #[cfg(any(target_os = "macos", target_os = "linux"))]
        {
            // Observe the exit without reaping the group leader. It remains
            // safe to signal this group if a descendant survives its shell.
            let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
            let result = unsafe {
                libc::waitid(
                    libc::P_PID,
                    self.child.id(),
                    &mut info,
                    libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
                )
            };
            if result == 0 {
                Ok(unsafe { info.si_pid() } == 0)
            } else {
                let error = io::Error::last_os_error();
                if error.kind() == io::ErrorKind::Interrupted {
                    return Ok(true);
                }
                Err(error)
            }
        }
        #[cfg(not(any(target_os = "macos", target_os = "linux")))]
        Ok(self.child.try_wait()?.is_none())
    }

    fn finish(&mut self) -> io::Result<ExitStatus> {
        #[cfg(any(target_os = "macos", target_os = "linux"))]
        {
            unsafe {
                libc::kill(-(self.child.id() as libc::pid_t), libc::SIGKILL);
            }
        }
        #[cfg(windows)]
        drop(self.job.take());
        let _ = self.child.kill();
        let status = self.child.wait();
        self.finished = true;
        status
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        if !self.finished {
            let _ = self.finish();
        }
    }
}
