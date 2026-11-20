use crate::protocol::{Address, Result};
use fs2::FileExt;
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    path::PathBuf,
};

pub fn home() -> Result<PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
        .map(PathBuf::from)
        .ok_or("home directory unavailable".into())
}
pub fn directory() -> Result<PathBuf> {
    let p = home()?.join(".agenterm");
    private_dir(&p)?;
    Ok(p)
}
pub fn installer_workspace() -> Option<PathBuf> {
    let root = std::env::temp_dir().canonicalize().ok()?;
    let executable = std::env::current_exe()
        .and_then(|path| path.canonicalize())
        .ok()?;
    let workspace = executable.parent()?;
    (workspace.parent() == Some(root.as_path())
        && workspace
            .file_name()?
            .to_str()?
            .starts_with("agenterm-assistance."))
    .then(|| workspace.to_owned())
}
pub fn private_dir(p: &std::path::Path) -> Result<()> {
    fs::create_dir_all(p).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(p, fs::Permissions::from_mode(0o700)).map_err(|e| e.to_string())?;
    }
    Ok(())
}
pub fn open_private(p: &std::path::Path) -> Result<File> {
    let mut o = OpenOptions::new();
    o.create(true).truncate(false).read(true).write(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    o.open(p).map_err(|e| e.to_string())
}
pub fn lock(a: &Address) -> Result<File> {
    let dir = directory()?.join("locks");
    private_dir(&dir)?;
    let host = a.host.trim_end_matches('.').to_ascii_lowercase();
    let host = if host.contains(':') {
        format!("[{host}]")
    } else {
        host
    };
    let mut key = format!("{}://{host}:{}", a.scheme, a.port);
    if a.path != "/agenterm/client" {
        key.push_str(&a.path);
    }
    let digest = Sha256::digest(key.as_bytes());
    let key: String = digest.iter().map(|b| format!("{b:02x}")).collect();
    let f = open_private(&dir.join(format!("{key}.lock")))?;
    f.try_lock_exclusive().map_err(|_| {
        crate::ui::text(
            "Client is already connected to this server.",
            "客户端已连接到此服务器。",
        )
        .to_string()
    })?;
    Ok(f)
}
pub fn history() -> Result<Vec<String>> {
    let p = home()?.join(".agenterm").join("servers.json");
    let raw = match fs::read(p) {
        Ok(b) if b.len() <= 65536 => b,
        Ok(_) => return Err("server history too large".into()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(e) => return Err(e.to_string()),
    };
    let values: Vec<String> = serde_json::from_slice(&raw).map_err(|e| e.to_string())?;
    Ok(values
        .into_iter()
        .filter(|s| Address::parse(s).is_ok())
        .take(10)
        .collect())
}
pub fn remember(a: &Address) -> Result<()> {
    let dir = directory()?;
    let lock = open_private(&dir.join("servers.lock"))?;
    lock.try_lock_exclusive().map_err(|e| e.to_string())?;
    let server = a.server();
    let mut values = vec![server.clone()];
    values.extend(history()?.into_iter().filter(|s| *s != server).take(9));
    let pending = dir.join(format!(".servers-rust-{}.tmp", std::process::id()));
    let mut f = open_private(&pending)?;
    f.set_len(0).map_err(|e| e.to_string())?;
    use std::io::Write;
    f.write_all(&serde_json::to_vec(&values).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    drop(f);
    fs::rename(&pending, dir.join("servers.json")).map_err(|e| e.to_string())?;
    Ok(())
}
