use super::*;
#[cfg(target_os = "linux")]
use std::process::Command;

#[cfg(target_os = "linux")]
pub(super) fn register(executable: &Path) -> Result<()> {
    let data = match std::env::var_os("XDG_DATA_HOME").filter(|p| !p.is_empty()) {
        Some(path) => PathBuf::from(path),
        None => storage::home()?.join(".local/share"),
    };
    if !data.is_absolute() {
        return Err("XDG_DATA_HOME must be absolute".into());
    }
    let applications = data.join("applications");
    write_desktop(&applications, executable)?;
    let output = Command::new("xdg-mime")
        .args([
            "default",
            "agenterm-cli.desktop",
            "x-scheme-handler/agenterm-cli",
        ])
        .output()
        .map_err(|e| e.to_string())?;
    if !output.status.success() {
        return Err(format!(
            "register link handler: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}
#[cfg(unix)]
pub(super) fn write_desktop(applications: &Path, executable: &Path) -> Result<()> {
    let mut directory = fs::DirBuilder::new();
    directory.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        directory.mode(0o755);
    }
    directory.create(applications).map_err(|e| e.to_string())?;
    let pending = applications.join(format!(
        ".agenterm-cli-{}.desktop",
        crate::terminal::random(12)?
    ));
    let result = (|| -> Result<()> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o644);
        }
        let mut file = options.open(&pending).map_err(|e| e.to_string())?;
        file.write_all(desktop_entry(executable)?.as_bytes())
            .map_err(|e| e.to_string())?;
        file.sync_all().map_err(|e| e.to_string())?;
        drop(file);
        fs::rename(&pending, applications.join("agenterm-cli.desktop")).map_err(|e| e.to_string())
    })();
    let _ = fs::remove_file(pending);
    result
}
pub(super) fn desktop_entry(executable: &Path) -> Result<String> {
    let path = executable
        .to_str()
        .ok_or("link handler path is not UTF-8")?;
    // Desktop entries first unescape string values, then parse Exec quoting.
    // Thus the quoting backslashes themselves need a second escape layer.
    let mut quoted = String::new();
    for c in path.chars() {
        match c {
            '\\' => quoted.push_str("\\\\\\\\"),
            '"' | '`' | '$' => {
                quoted.push_str("\\\\");
                quoted.push(c);
            }
            '%' => quoted.push_str("%%"),
            '\n' => quoted.push_str("\\n"),
            '\r' => quoted.push_str("\\r"),
            '\t' => quoted.push_str("\\t"),
            c => quoted.push(c),
        }
    }
    if path.contains('=') {
        return Err("desktop executable path cannot contain '='".into());
    }
    Ok(format!(
        "[Desktop Entry]\nType=Application\nName=Agenterm CLI\nExec=\"{quoted}\" %u\nTerminal=true\nNoDisplay=true\nMimeType=x-scheme-handler/agenterm-cli;\n"
    ))
}
