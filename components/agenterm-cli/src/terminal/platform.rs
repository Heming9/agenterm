use super::{Result, Setup, Terminal};
use std::{
    io::{Read, Write},
    path::Path,
    process::{Child, Command, Stdio},
};

pub struct Spawned {
    pub child: Child,
    pub input: Box<dyn Write + Send>,
    pub output: Box<dyn Read + Send>,
    #[cfg(unix)]
    pub pty: std::fs::File,
    #[cfg(windows)]
    pub job: Job,
}
pub fn hostname() -> String {
    #[cfg(unix)]
    {
        let mut buf = [0u8; 256];
        // The buffer is writable and gethostname receives its actual capacity.
        if unsafe { libc::gethostname(buf.as_mut_ptr().cast(), buf.len()) } == 0 {
            return String::from_utf8_lossy(
                &buf[..buf.iter().position(|b| *b == 0).unwrap_or(buf.len())],
            )
            .into();
        }
    }
    std::env::var("COMPUTERNAME").unwrap_or_else(|_| "unknown".into())
}
#[cfg(unix)]
pub fn username() -> String {
    let uid = unsafe { libc::geteuid() };
    let mut record = std::mem::MaybeUninit::<libc::passwd>::uninit();
    let mut buffer = [0u8; 16 * 1024];
    let mut found = std::ptr::null_mut();
    // On success, found points to the record and pw_name points into buffer.
    let status = unsafe {
        libc::getpwuid_r(
            uid,
            record.as_mut_ptr(),
            buffer.as_mut_ptr().cast(),
            buffer.len(),
            &mut found,
        )
    };
    if status == 0 && !found.is_null() {
        let name = unsafe { std::ffi::CStr::from_ptr((*found).pw_name) };
        return name.to_string_lossy().into_owned();
    }
    uid.to_string()
}
#[cfg(windows)]
pub fn username() -> String {
    use windows_sys::Win32::System::WindowsProgramming::GetUserNameW;

    // GetUserNameW requires space for up to 256 characters and a NUL.
    let mut buffer = [0u16; 257];
    let mut len = buffer.len() as u32;
    if unsafe { GetUserNameW(buffer.as_mut_ptr(), &mut len) } != 0 {
        return String::from_utf16_lossy(&buffer[..len.saturating_sub(1) as usize]);
    }
    std::env::var("USERNAME").unwrap_or_else(|_| "unknown".into())
}
pub fn shell() -> String {
    #[cfg(unix)]
    {
        let shell = std::env::var("SHELL").unwrap_or_else(|_| "/bin/sh".into());
        let name = Path::new(&shell)
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("");
        if ["sh", "bash", "zsh"].contains(&name) && Path::new(&shell).is_absolute() {
            return shell;
        }
        "/bin/sh".into()
    }
    #[cfg(windows)]
    {
        std::env::var("AGENTERM_POWERSHELL").unwrap_or_else(|_| "powershell.exe".into())
    }
}
fn quote(s: &str) -> String {
    #[cfg(unix)]
    {
        format!("'{}'", s.replace('\'', "'\\''"))
    }
    #[cfg(windows)]
    {
        format!("'{}'", s.replace('\'', "''"))
    }
}
pub fn source_line(p: &Path) -> String {
    #[cfg(unix)]
    {
        format!(". {}\n", quote(&p.to_string_lossy()))
    }
    #[cfg(windows)]
    {
        format!(
            ". ([scriptblock]::Create([System.IO.File]::ReadAllText({})))\n",
            quote(&p.to_string_lossy())
        )
    }
}
pub fn command_script(command: &str) -> String {
    #[cfg(unix)]
    {
        format!("{command}\n")
    }
    #[cfg(windows)]
    {
        format!(
            "\u{feff}{command}\n\n$agenterm_command_ok = $?\n$global:agenterm_command_code = if ($agenterm_command_ok) {{ 0 }} elseif ($Error.Count -and $Error[0] -ne $global:agenterm_error_before -and $Error[0].FullyQualifiedErrorId -notlike 'NativeCommandError*') {{ 1 }} elseif ($global:LASTEXITCODE) {{ $global:LASTEXITCODE }} else {{ 1 }}\n"
        )
    }
}
fn command(setup: Option<&Setup>) -> Command {
    let mut cmd = Command::new(shell());
    if let Some(setup) = setup {
        if !setup.cwd.is_empty() {
            cmd.current_dir(&setup.cwd);
        }
        cmd.envs(&setup.env);
    }
    cmd
}

#[cfg(unix)]
pub fn spawn(marker: &[u8], setup: Option<&Setup>) -> Result<Spawned> {
    use std::{
        fs::File,
        os::{
            fd::{AsRawFd, FromRawFd},
            unix::process::CommandExt,
        },
    };
    let mut master = -1;
    let mut slave = -1;
    // openpty initializes both descriptors or reports failure. Each successful
    // descriptor is immediately transferred to exactly one owning File.
    if unsafe {
        libc::openpty(
            &mut master,
            &mut slave,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            std::ptr::null_mut(),
        )
    } != 0
    {
        return Err(std::io::Error::last_os_error().to_string());
    }
    let master = unsafe { File::from_raw_fd(master) };
    let slave = unsafe { File::from_raw_fd(slave) };
    unsafe {
        for fd in [master.as_raw_fd(), slave.as_raw_fd()] {
            libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
        }
        let mut settings = std::mem::zeroed();
        if libc::tcgetattr(slave.as_raw_fd(), &mut settings) != 0 {
            return Err(std::io::Error::last_os_error().to_string());
        }
        settings.c_lflag &= !(libc::ECHO | libc::ECHONL);
        if libc::tcsetattr(slave.as_raw_fd(), libc::TCSANOW, &settings) != 0 {
            return Err(std::io::Error::last_os_error().to_string());
        }
    }
    let mut cmd = command(setup);
    let path = shell();
    let name = Path::new(&path).file_name().unwrap().to_string_lossy();
    match name.as_ref() {
        "bash" => {
            cmd.args(["--noprofile", "--norc", "-i"]);
        }
        "zsh" => {
            cmd.args([
                "-f",
                "-d",
                "-i",
                "-o",
                "nozle",
                "-o",
                "nopromptcr",
                "-o",
                "nopromptsp",
            ]);
        }
        _ => {
            cmd.arg("-i");
        }
    }
    cmd.envs([
        ("PS1", ""),
        ("PS2", ""),
        ("PROMPT_COMMAND", ""),
        ("ENV", ""),
        ("BASH_ENV", ""),
        ("BASH_SILENCE_DEPRECATION_WARNING", "1"),
        ("PROMPT_EOL_MARK", ""),
    ]);
    cmd.stdin(Stdio::from(slave.try_clone().map_err(|e| e.to_string())?))
        .stdout(Stdio::from(slave.try_clone().map_err(|e| e.to_string())?))
        .stderr(Stdio::from(slave));
    unsafe {
        cmd.pre_exec(|| {
            if libc::setsid() < 0 {
                return Err(std::io::Error::last_os_error());
            }
            if libc::ioctl(0, libc::TIOCSCTTY as _, 0) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            for sig in [
                libc::SIGINT,
                libc::SIGQUIT,
                libc::SIGTERM,
                libc::SIGPIPE,
                libc::SIGTSTP,
                libc::SIGTTIN,
                libc::SIGTTOU,
            ] {
                libc::signal(sig, libc::SIG_DFL);
            }
            Ok(())
        });
    }
    let mut child = cmd.spawn().map_err(|e| e.to_string())?;
    let mut input = master.try_clone().map_err(|e| e.to_string())?;
    let marker = String::from_utf8_lossy(marker);
    let init = if name == "zsh" {
        let prompt = marker.replace('\x1e', "\\036") + "%?\\037";
        format!(
            "unset PS1 PS2 PROMPT_COMMAND; PS1=$(printf '%b' {}); PS2=''\n",
            quote(&prompt)
        )
    } else {
        let prefix = marker.replace('\x1e', "\\036");
        format!(
            "unset PS1 PS2 PROMPT_COMMAND agenterm_prompt; agenterm_prompt=$(printf '%b' {}); PS1=$(printf '%b' '${{agenterm_prompt:+${{agenterm_prompt}}${{?}}\\037}}'); PS2=''\n",
            quote(&prefix)
        )
    };
    if let Err(e) = input.write_all(init.as_bytes()) {
        let _ = child.kill();
        let _ = child.wait();
        return Err(e.to_string());
    }
    // Nonblocking reads allow shutdown to drain the PTY without waiting forever
    // on a descriptor inherited by a background descendant.
    unsafe {
        let flags = libc::fcntl(master.as_raw_fd(), libc::F_GETFL);
        if flags < 0 || libc::fcntl(master.as_raw_fd(), libc::F_SETFL, flags | libc::O_NONBLOCK) < 0
        {
            let _ = child.kill();
            let _ = child.wait();
            return Err(std::io::Error::last_os_error().to_string());
        }
    }
    Ok(Spawned {
        child,
        input: Box::new(input),
        output: Box::new(master.try_clone().map_err(|e| e.to_string())?),
        pty: master,
    })
}

#[cfg(unix)]
pub fn control(t: &Terminal, action: &str) -> Result<()> {
    use std::os::fd::AsRawFd;
    let fd = t.pty.as_raw_fd();
    unsafe {
        if action == "interrupt" {
            let group = libc::tcgetpgrp(fd);
            if group <= 0 || libc::kill(-group, libc::SIGINT) != 0 {
                return Err(std::io::Error::last_os_error().to_string());
            }
            return Ok(());
        }
        let mut settings = std::mem::zeroed();
        if libc::tcgetattr(fd, &mut settings) != 0 {
            return Err(std::io::Error::last_os_error().to_string());
        }
        let eof = settings.c_cc[libc::VEOF];
        if settings.c_lflag & libc::ICANON == 0
            || libc::c_long::from(eof) == libc::fpathconf(fd, libc::_PC_VDISABLE)
        {
            return Err("EOF requires canonical terminal input".into());
        }
        t.write(&[eof])
    }
}

#[cfg(unix)]
pub fn stop(t: &Terminal, child: &mut Child) {
    use std::os::fd::AsRawFd;
    let pid = child.id() as i32;
    // Interactive jobs can occupy separate process groups. Include descendants
    // before killing the controlling shell, then signal its group as a fallback.
    if let Ok(out) = Command::new("ps").args(["-axo", "pid=,ppid="]).output() {
        let pairs: Vec<(i32, i32)> = String::from_utf8_lossy(&out.stdout)
            .lines()
            .filter_map(|l| {
                let mut s = l.split_whitespace();
                Some((s.next()?.parse().ok()?, s.next()?.parse().ok()?))
            })
            .collect();
        let mut children = vec![pid];
        let mut i = 0;
        while i < children.len() {
            let parent = children[i];
            for &(p, pp) in &pairs {
                if pp == parent && p != pid && !children.contains(&p) {
                    children.push(p);
                }
            }
            i += 1;
        }
        for p in children.into_iter().skip(1).rev() {
            unsafe {
                libc::kill(p, libc::SIGKILL);
            }
        }
    }
    unsafe {
        let group = libc::tcgetpgrp(t.pty.as_raw_fd());
        if group > 0 {
            libc::kill(-group, libc::SIGKILL);
        }
        libc::kill(-pid, libc::SIGKILL);
    }
    let _ = child.kill();
}

#[cfg(windows)]
pub struct Job(windows_sys::Win32::Foundation::HANDLE);
#[cfg(windows)]
unsafe impl Send for Job {}
#[cfg(windows)]
unsafe impl Sync for Job {}
#[cfg(windows)]
impl Drop for Job {
    fn drop(&mut self) {
        unsafe {
            windows_sys::Win32::Foundation::CloseHandle(self.0);
        }
    }
}
#[cfg(windows)]
impl Job {
    pub(crate) fn assign(child: &Child) -> Result<Self> {
        use std::os::windows::io::AsRawHandle;
        use windows_sys::Win32::{Foundation::CloseHandle, System::JobObjects::*};
        unsafe {
            let handle = CreateJobObjectW(std::ptr::null(), std::ptr::null());
            if handle.is_null() {
                return Err(std::io::Error::last_os_error().to_string());
            }
            let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
            info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            if SetInformationJobObject(
                handle,
                JobObjectExtendedLimitInformation,
                (&info as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                std::mem::size_of_val(&info) as u32,
            ) == 0
                || AssignProcessToJobObject(handle, child.as_raw_handle()) == 0
            {
                let err = std::io::Error::last_os_error().to_string();
                CloseHandle(handle);
                return Err(err);
            }
            Ok(Self(handle))
        }
    }
}

#[cfg(windows)]
pub fn spawn(marker: &[u8], setup: Option<&Setup>) -> Result<Spawned> {
    use std::os::windows::process::CommandExt;
    let mut cmd = command(setup);
    let marker = quote(&String::from_utf8_lossy(marker));
    let script = format!(
        "$ProgressPreference='SilentlyContinue'; $global:agenterm_utf8=[System.Text.UTF8Encoding]::new($false); [Console]::InputEncoding=$agenterm_utf8; [Console]::OutputEncoding=$agenterm_utf8; $OutputEncoding=$agenterm_utf8; $global:agenterm_input=[Console]::OpenStandardInput(); function global:PSConsoleHostReadLine {{ $agenterm_bytes=[System.Collections.Generic.List[byte]]::new(); while (($agenterm_byte=$global:agenterm_input.ReadByte()) -ge 0) {{ if ($agenterm_byte -eq 10) {{break}}; $agenterm_bytes.Add([byte]$agenterm_byte) }}; if ($agenterm_byte -lt 0 -and $agenterm_bytes.Count -eq 0) {{exit}}; $global:agenterm_command_code=$null; $global:agenterm_error_before=$Error[0]; return $global:agenterm_utf8.GetString($agenterm_bytes.ToArray()).TrimEnd([char]13) }}; function global:prompt {{ $agenterm_ok=$?; $agenterm_code=if ($null -ne $global:agenterm_command_code) {{$global:agenterm_command_code}} elseif ($agenterm_ok) {{0}} else {{1}}; return ({marker} + $agenterm_code + [char]31) }}"
    );
    let (reader, writer) = std::io::pipe().map_err(|e| e.to_string())?;
    cmd.args(["-NoLogo", "-NoProfile", "-NoExit", "-Command", &script])
        .stdin(Stdio::piped())
        .stdout(Stdio::from(writer.try_clone().map_err(|e| e.to_string())?))
        .stderr(Stdio::from(writer));
    cmd.creation_flags(0x08000000);
    let mut child = cmd.spawn().map_err(|e| e.to_string())?;
    let job = match Job::assign(&child) {
        Ok(j) => j,
        Err(e) => {
            let _ = child.kill();
            let _ = child.wait();
            return Err(e);
        }
    };
    Ok(Spawned {
        input: Box::new(child.stdin.take().ok_or("missing shell stdin")?),
        output: Box::new(reader),
        child,
        job,
    })
}
#[cfg(windows)]
pub fn control(t: &Terminal, action: &str) -> Result<()> {
    if action != "eof" {
        return Err("unsupported session control".into());
    }
    t.input.lock().unwrap().take();
    Ok(())
}
#[cfg(windows)]
pub fn stop(t: &Terminal, _child: &mut Child) {
    unsafe {
        windows_sys::Win32::System::JobObjects::TerminateJobObject(t.job.0, 1);
    }
}
