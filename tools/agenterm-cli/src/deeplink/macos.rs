use super::*;
use std::process::Command;

pub(super) fn register(base: &Path, executable: &Path) -> Result<()> {
    let inbox = base.join("link-inbox");
    storage::private_dir(&inbox)?;
    let staging = base.join(format!(".cli-handler-{}", crate::terminal::random(12)?));
    storage::private_dir(&staging)?;
    let result = (|| -> Result<()> {
        let bundle = staging.join("Agenterm CLI.app");
        run(
            Command::new("/usr/bin/osacompile")
                .arg("-o")
                .arg(&bundle)
                .arg("-e")
                .arg(script(executable, &inbox)?),
            "compile link handler",
        )?;
        configure_bundle(&bundle)?;
        let destination = base.join("Agenterm CLI.app");
        let backup = staging.join("previous.app");
        let replaced = match fs::symlink_metadata(&destination) {
            Ok(info) if info.file_type().is_dir() => {
                fs::rename(&destination, &backup).map_err(|e| e.to_string())?;
                true
            }
            Ok(_) => return Err("existing Agenterm link handler is not a directory".into()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => false,
            Err(e) => return Err(e.to_string()),
        };
        if let Err(error) = fs::rename(&bundle, &destination) {
            if replaced {
                let _ = fs::rename(&backup, &destination);
            }
            return Err(error.to_string());
        }
        run(Command::new("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister").arg("-f").arg(&destination),"register link handler")
    })();
    let _ = fs::remove_dir_all(staging);
    result
}

fn quote(path: &Path) -> Result<String> {
    serde_json::to_string(path.to_str().ok_or("link handler path is not UTF-8")?)
        .map_err(|e| e.to_string())
}
pub(super) fn script(executable: &Path, inbox: &Path) -> Result<String> {
    Ok(format!(
        r#"on open location incomingURL
    set fileTemplate to {}
    set filePath to do shell script "umask 077; f=$(/usr/bin/mktemp " & quoted form of fileTemplate & "); /usr/bin/printf %s " & quoted form of incomingURL & " > \"$f\"; /usr/bin/printf %s \"$f\""
    tell application "Terminal"
        activate
        do script (quoted form of {} & " -link-file " & quoted form of filePath)
    end tell
end open location"#,
        quote(&inbox.join("agenterm-link.XXXXXXXXXX"))?,
        quote(executable)?
    ))
}
pub(super) fn configure_bundle(bundle: &Path) -> Result<()> {
    let plist = bundle.join("Contents/Info.plist");
    for args in [
        vec![
            "-replace",
            "CFBundleIdentifier",
            "-string",
            "io.agenterm.cli.link",
        ],
        vec![
            "-insert",
            "CFBundleURLTypes",
            "-json",
            r#"[{"CFBundleURLSchemes":["agenterm-cli"]}]"#,
        ],
        vec![
            "-replace",
            "NSAppleEventsUsageDescription",
            "-string",
            "Agenterm opens Terminal to start the requested connection.",
        ],
    ] {
        run(
            Command::new("/usr/bin/plutil").args(args).arg(&plist),
            "configure link handler",
        )?;
    }
    run(
        Command::new("/usr/bin/codesign")
            .args(["--force", "--deep", "--sign", "-"])
            .arg(bundle),
        "sign link handler",
    )
}
fn run(command: &mut Command, description: &str) -> Result<()> {
    let output = command
        .output()
        .map_err(|e| format!("{description}: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "{description}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}
