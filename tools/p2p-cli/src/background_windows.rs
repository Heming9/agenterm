//! Detached workers inherit no handles, including nonstandard capture pipes.
use std::{
    ffi::OsStr,
    fs::OpenOptions,
    io,
    os::windows::{
        ffi::OsStrExt,
        io::{AsRawHandle, FromRawHandle, IntoRawHandle, OwnedHandle},
        process::ExitStatusExt,
    },
    path::Path,
    process::ExitStatus,
};
use windows_sys::Win32::{
    Foundation::{WAIT_FAILED, WAIT_OBJECT_0, WAIT_TIMEOUT},
    System::{
        Console::{STD_ERROR_HANDLE, SetStdHandle},
        Threading::{
            CREATE_NEW_PROCESS_GROUP, CreateProcessW, DETACHED_PROCESS, GetExitCodeProcess,
            INFINITE, PROCESS_INFORMATION, STARTUPINFOW, TerminateProcess, WaitForSingleObject,
        },
    },
};

pub struct Child(OwnedHandle);

impl Child {
    pub fn try_wait(&mut self) -> io::Result<Option<ExitStatus>> {
        match unsafe { WaitForSingleObject(self.0.as_raw_handle(), 0) } {
            WAIT_TIMEOUT => Ok(None),
            WAIT_OBJECT_0 => self.status().map(Some),
            _ => Err(io::Error::last_os_error()),
        }
    }

    fn status(&self) -> io::Result<ExitStatus> {
        let mut code = 0;
        if unsafe { GetExitCodeProcess(self.0.as_raw_handle(), &mut code) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(ExitStatus::from_raw(code))
    }

    pub fn kill(&mut self) -> io::Result<()> {
        if unsafe { TerminateProcess(self.0.as_raw_handle(), 1) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn wait(&mut self) -> io::Result<ExitStatus> {
        if unsafe { WaitForSingleObject(self.0.as_raw_handle(), INFINITE) } == WAIT_FAILED {
            return Err(io::Error::last_os_error());
        }
        self.status()
    }
}

fn quote(argument: &OsStr) -> io::Result<Vec<u16>> {
    let mut result = vec![b'"' as u16];
    let mut slashes = 0;
    for unit in argument.encode_wide() {
        if unit == 0 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "NUL in process argument",
            ));
        }
        if unit == b'\\' as u16 {
            slashes += 1;
            continue;
        }
        result.extend(std::iter::repeat_n(
            b'\\' as u16,
            slashes * if unit == b'"' as u16 { 2 } else { 1 },
        ));
        if unit == b'"' as u16 {
            result.push(b'\\' as u16);
        }
        result.push(unit);
        slashes = 0;
    }
    result.extend(std::iter::repeat_n(b'\\' as u16, slashes * 2));
    result.push(b'"' as u16);
    Ok(result)
}

pub fn spawn(config: &Path, directory: &Path) -> io::Result<Child> {
    let executable = std::env::current_exe()?;
    let application: Vec<u16> = executable
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect();
    let mut command = Vec::new();
    for argument in [
        executable.as_os_str(),
        OsStr::new("_serve"),
        OsStr::new("--config"),
        config.as_os_str(),
        OsStr::new("--state-dir"),
        directory.as_os_str(),
    ] {
        if !command.is_empty() {
            command.push(b' ' as u16);
        }
        command.extend(quote(argument)?);
    }
    command.push(0);
    let startup = STARTUPINFOW {
        cb: size_of::<STARTUPINFOW>() as u32,
        ..unsafe { std::mem::zeroed() }
    };
    let mut information: PROCESS_INFORMATION = unsafe { std::mem::zeroed() };
    // The stable Rust Command API cannot disable handle inheritance. Do not
    // supply STARTF_USESTDHANDLES: the worker opens its own log after startup.
    let created = unsafe {
        CreateProcessW(
            application.as_ptr(),
            command.as_mut_ptr(),
            std::ptr::null(),
            std::ptr::null(),
            0,
            DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP,
            std::ptr::null(),
            std::ptr::null(),
            &startup,
            &mut information,
        )
    };
    if created == 0 {
        return Err(io::Error::last_os_error());
    }
    // Both handles are owned by this successful CreateProcess call.
    drop(unsafe { OwnedHandle::from_raw_handle(information.hThread) });
    Ok(Child(unsafe {
        OwnedHandle::from_raw_handle(information.hProcess)
    }))
}

pub fn attach_log(directory: &Path) -> io::Result<()> {
    let log = OpenOptions::new()
        .create(true)
        .append(true)
        .open(directory.join("daemon.log"))?;
    if unsafe { SetStdHandle(STD_ERROR_HANDLE, log.as_raw_handle()) } == 0 {
        return Err(io::Error::last_os_error());
    }
    // Transfer the log handle to the process standard-error slot until exit.
    let _ = log.into_raw_handle();
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quotes_unicode_paths_backslashes_and_quotes() {
        for (input, expected) in [
            ("", "\"\""),
            ("with space", "\"with space\""),
            (r"C:\路径\", r#""C:\路径\\""#),
            (r#"a\"b"#, r#""a\\\"b""#),
        ] {
            assert_eq!(
                String::from_utf16(&quote(OsStr::new(input)).unwrap()).unwrap(),
                expected
            );
        }
        assert!(quote(OsStr::new("bad\0argument")).is_err());
    }
}
