//! Protocol launches use per-user registration, a stable executable, and private
//! macOS link files. Legacy URL mode parameters do not select a connection mode.
#[cfg(any(target_os = "linux", test))]
mod linux;
#[cfg(target_os = "macos")]
mod macos;
#[cfg(test)]
mod tests;
#[cfg(windows)]
mod windows;

use crate::{
    protocol::{self, Result},
    storage,
};
use fs2::FileExt;
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{Read, Seek, Write},
    path::{Path, PathBuf},
};

pub const SCHEME: &str = "agenterm-cli";
const MAX_LINK: u64 = 8192;
const INVALID: &str = "invalid Agenterm link";

#[derive(Debug, PartialEq, Eq)]
pub struct Link {
    pub address: String,
}

pub fn parse(raw: &str) -> Result<Link> {
    if raw.len() as u64 > MAX_LINK || raw.contains('#') || raw.bytes().any(|b| b.is_ascii_control())
    {
        return Err(INVALID.into());
    }
    let (endpoint, query) = raw.split_once('?').ok_or(INVALID)?;
    let endpoint = endpoint.strip_prefix(SCHEME).ok_or(INVALID)?;
    if !matches!(
        endpoint,
        "://authorize" | "://authorize/" | "://connect" | "://connect/"
    ) {
        return Err(INVALID.into());
    }
    let mut address = None;
    let mut mode = None;
    for part in query.split('&').filter(|p| !p.is_empty()) {
        if part.contains(';') {
            return Err(INVALID.into());
        }
        let (key, value) = part.split_once('=').unwrap_or((part, ""));
        let key = decode_query(key)?;
        let value = decode_query(value)?;
        match key.as_str() {
            "url" if address.is_none() => address = Some(value),
            "mode" if mode.is_none() && matches!(value.as_str(), "temporary" | "persistent") => {
                mode = Some(value)
            }
            _ => return Err(INVALID.into()),
        }
    }
    let address = address.ok_or(INVALID)?;
    // Scheme omission is valid in the interactive form, but not in a protocol URL.
    if !address.contains("://") || protocol::connection_address(address.trim()).is_err() {
        return Err(INVALID.into());
    }
    Ok(Link { address })
}
fn decode_query(raw: &str) -> Result<String> {
    fn hex(b: u8) -> Option<u8> {
        match b {
            b'0'..=b'9' => Some(b - b'0'),
            b'a'..=b'f' => Some(b - b'a' + 10),
            b'A'..=b'F' => Some(b - b'A' + 10),
            _ => None,
        }
    }
    let mut out = Vec::with_capacity(raw.len());
    let mut bytes = raw.bytes();
    while let Some(b) = bytes.next() {
        out.push(match b {
            b'+' => b' ',
            b'%' => {
                let a = bytes.next().and_then(hex).ok_or(INVALID)?;
                let b = bytes.next().and_then(hex).ok_or(INVALID)?;
                a * 16 + b
            }
            b => b,
        });
    }
    String::from_utf8(out).map_err(|_| INVALID.into())
}

pub fn is_launch(args: &[String]) -> bool {
    (args.len() == 1 && args[0].starts_with("agenterm-cli:"))
        || (args.len() == 2 && args[0] == "-link-file")
}
pub fn arguments(args: Vec<String>) -> Result<Vec<String>> {
    let raw = if args.len() == 1 && args[0].starts_with("agenterm-cli:") {
        Some(args[0].clone())
    } else if args.len() == 2 && args[0] == "-link-file" {
        Some(consume_link_file(
            Path::new(&args[1]),
            &config_dir()?.join("Agenterm/link-inbox"),
        )?)
    } else {
        None
    };
    let Some(raw) = raw else {
        return Ok(args);
    };
    let link = parse(&raw)?;
    Ok(vec!["-token".into(), link.address])
}

fn config_dir() -> Result<PathBuf> {
    #[cfg(target_os = "macos")]
    let path = storage::home()?.join("Library/Application Support");
    #[cfg(windows)]
    let path = std::env::var_os("APPDATA")
        .filter(|p| !p.is_empty())
        .map(PathBuf::from)
        .ok_or("APPDATA is not defined")?;
    #[cfg(not(any(target_os = "macos", windows)))]
    let path = match std::env::var_os("XDG_CONFIG_HOME").filter(|p| !p.is_empty()) {
        Some(path) => PathBuf::from(path),
        None => storage::home()?.join(".config"),
    };
    if !path.is_absolute() {
        return Err("user configuration directory must be absolute".into());
    }
    Ok(path)
}

// Registration is best-effort in main, as in Go. URL launches skip it so an
// already-running macOS handler bundle is never replaced by its own launch.
pub fn register() -> Result<()> {
    let base = config_dir()?.join("Agenterm");
    storage::private_dir(&base)?;
    let lock = storage::open_private(&base.join("cli-link.lock"))?;
    lock.lock_exclusive().map_err(|e| e.to_string())?;
    let executable = copy_executable(
        &std::env::current_exe().map_err(|e| e.to_string())?,
        &base.join("cli"),
    )?;
    #[cfg(target_os = "macos")]
    return macos::register(&base, &executable);
    #[cfg(target_os = "linux")]
    return linux::register(&executable);
    #[cfg(windows)]
    return windows::register(&executable);
    #[cfg(not(any(target_os = "macos", target_os = "linux", windows)))]
    {
        let _ = executable;
        Ok(())
    }
}

pub(crate) fn hash_reader(file: &mut File) -> Result<String> {
    let mut hash = Sha256::new();
    let mut buffer = [0; 65536];
    loop {
        let n = file.read(&mut buffer).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        hash.update(&buffer[..n]);
    }
    Ok(format!("{:x}", hash.finalize()))
}
fn copy_executable(source: &Path, dir: &Path) -> Result<PathBuf> {
    let mut input = File::open(source).map_err(|e| e.to_string())?;
    let digest = hash_reader(&mut input)?;
    let name = format!(
        "agenterm-cli-{digest}{}",
        if cfg!(windows) { ".exe" } else { "" }
    );
    storage::private_dir(dir)?;
    let destination = dir.join(name);
    match fs::symlink_metadata(&destination) {
        Ok(info) if !info.file_type().is_file() => {
            return Err("existing Agenterm link handler is not a regular file".into());
        }
        Ok(_) => {
            if hash_reader(&mut File::open(&destination).map_err(|e| e.to_string())?)? != digest {
                return Err("existing Agenterm link handler has an unexpected checksum".into());
            }
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fs::set_permissions(&destination, fs::Permissions::from_mode(0o700))
                    .map_err(|e| e.to_string())?;
            }
            return Ok(destination);
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e.to_string()),
    }
    input.rewind().map_err(|e| e.to_string())?;
    let pending = dir.join(format!(".agenterm-cli-{}", crate::terminal::random(12)?));
    let result = (|| -> Result<()> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o700);
        }
        let mut output = options.open(&pending).map_err(|e| e.to_string())?;
        let mut hash = Sha256::new();
        let mut buffer = [0; 65536];
        loop {
            let n = input.read(&mut buffer).map_err(|e| e.to_string())?;
            if n == 0 {
                break;
            }
            output.write_all(&buffer[..n]).map_err(|e| e.to_string())?;
            hash.update(&buffer[..n]);
        }
        if format!("{:x}", hash.finalize()) != digest {
            return Err("Agenterm executable changed during registration".into());
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            output
                .set_permissions(fs::Permissions::from_mode(0o700))
                .map_err(|e| e.to_string())?;
        }
        output.sync_all().map_err(|e| e.to_string())?;
        drop(output);
        fs::rename(&pending, &destination).map_err(|e| e.to_string())
    })();
    let _ = fs::remove_file(pending);
    result?;
    Ok(destination)
}

fn consume_link_file(path: &Path, dir: &Path) -> Result<String> {
    let invalid = || "invalid link file".to_string();
    if path.parent() != Some(dir)
        || !path
            .file_name()
            .and_then(|s| s.to_str())
            .is_some_and(|s| s.starts_with("agenterm-link."))
    {
        return Err(invalid());
    }
    let entry = fs::symlink_metadata(path).map_err(|_| invalid())?;
    if !entry.file_type().is_file() {
        return Err(invalid());
    }
    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK);
    }
    let file = options.open(path).map_err(|_| invalid())?;
    let info = file.metadata().map_err(|_| invalid())?;
    if !info.file_type().is_file() {
        return Err(invalid());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if info.mode() & 0o077 != 0
            || info.uid() != unsafe { libc::geteuid() }
            || info.dev() != entry.dev()
            || info.ino() != entry.ino()
        {
            return Err(invalid());
        }
    }
    let mut bytes = Vec::new();
    file.take(MAX_LINK + 1)
        .read_to_end(&mut bytes)
        .map_err(|_| invalid())?;
    if bytes.len() as u64 > MAX_LINK {
        return Err(invalid());
    }
    fs::remove_file(path).map_err(|e| e.to_string())?;
    let raw = String::from_utf8(bytes).map_err(|_| invalid())?;
    parse(&raw)?;
    Ok(raw)
}
