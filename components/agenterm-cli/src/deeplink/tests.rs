use super::*;
use crate::ui::Options;

struct Temp(PathBuf);
impl Temp {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "agenterm-link-test-{}-{}",
            std::process::id(),
            crate::terminal::random(12).unwrap()
        ));
        storage::private_dir(&path).unwrap();
        Self(path)
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
fn escape(raw: &str) -> String {
    raw.bytes()
        .map(|b| {
            if b.is_ascii_alphanumeric() || b"-_.~".contains(&b) {
                (b as char).to_string()
            } else {
                format!("%{b:02X}")
            }
        })
        .collect()
}
fn link(address: &str, mode: Option<&str>) -> String {
    format!(
        "agenterm-cli://connect?url={}{}",
        escape(address),
        mode.map_or(String::new(), |m| format!("&mode={m}"))
    )
}
fn private_file(path: &Path, contents: &str) {
    let mut file = storage::open_private(path).unwrap();
    file.set_len(0).unwrap();
    file.write_all(contents.as_bytes()).unwrap();
}

#[test]
fn links_normalize_to_the_same_options_as_go() {
    for address in [
        "https://example.com/base/c/ab1234",
        "http://127.0.0.1:12345/c/ABC123",
        "https://[::1]:443/a-b/c/ab1234",
    ] {
        for mode in [None, Some("temporary"), Some("persistent")] {
            for slash in [false, true] {
                let mut raw = link(address, mode);
                if slash {
                    raw = raw.replacen("connect?", "connect/?", 1);
                }
                assert_eq!(
                    parse(&raw).unwrap(),
                    Link {
                        address: address.into(),
                        mode: mode.map(String::from)
                    }
                );
                let args = vec![raw];
                assert!(is_launch(&args));
                let normalized = arguments(args).unwrap();
                let options = Options::parse(normalized).unwrap().unwrap();
                assert_eq!(options.token, address);
                assert!(options.token_provided);
                assert!(options.invite.is_empty());
                assert_eq!(options.temporary, mode != Some("persistent"));
                assert_eq!(options.mode_provided, mode.is_some());
            }
        }
    }
    let args = vec!["-token".into(), "test".into()];
    assert!(!is_launch(&args));
    assert_eq!(arguments(args.clone()).unwrap(), args);
}

#[test]
fn malformed_or_injected_links_never_become_command_flags() {
    let address = escape("https://example.com/c/ab1234");
    for raw in [
        format!("agenterm://connect?url={address}"),
        format!("agenterm-cli://connect.evil?url={address}"),
        format!("agenterm-cli://user@connect?url={address}"),
        format!("agenterm-cli://connect:80?url={address}"),
        format!("agenterm-cli://connect/path?url={address}"),
        format!("agenterm-cli:connect?url={address}"),
        format!("agenterm-cli://connect?url={address}#fragment"),
        format!("agenterm-cli://connect?url={address}&extra=x"),
        format!("agenterm-cli://connect?url={address}&mode=other"),
        format!("agenterm-cli://connect?url={address}&mode="),
        format!("agenterm-cli://connect?url={address}&mode=temporary&mode=persistent"),
        format!("agenterm-cli://connect?url={address}&url={address}"),
        format!("agenterm-cli://connect?url={address};mode=temporary"),
        "agenterm-cli://connect?url=file%3A%2F%2Fetc%2Fpasswd".into(),
        "agenterm-cli://connect?url=example.com%2Fc%2Fab1234".into(),
        "agenterm-cli://connect?url=Agenterm_fake".into(),
        "agenterm-cli://connect?url=%zz".into(),
        "agenterm-cli://connect?url=%FF".into(),
        "agenterm-cli://connect?url=%".into(),
        link("https://example.com/c/ab1234?command=rm", None),
        link("https://example.com/c/ab1234\n-token malicious", None),
        "agenterm-cli://connect?url=".into(),
        "agenterm-cli://connect".into(),
        format!("agenterm-cli://connect?url={address}&%6dode=persistent&mode=temporary"),
        "agenterm-cli://connect?url=--token+malicious".into(),
        format!("agenterm-cli://connect?url={address}&{}", "x".repeat(8192)),
    ] {
        assert!(parse(&raw).is_err(), "accepted {raw}");
    }
    let mixed = vec![
        link("https://example.com/c/ab1234", None),
        "-connection-mode".into(),
        "persistent".into(),
    ];
    assert!(Options::parse(arguments(mixed).unwrap()).is_err());
}

#[test]
fn private_link_file_is_consumed_once_and_preserves_mode() {
    let dir = Temp::new();
    let path = dir.0.join("agenterm-link.12345");
    let raw = link("https://example.com/base/c/ab1234", Some("persistent"));
    private_file(&path, &raw);
    assert_eq!(consume_link_file(&path, &dir.0).unwrap(), raw);
    assert!(!path.exists());
    assert!(consume_link_file(&path, &dir.0).is_err());
    private_file(&path, "agenterm-cli://connect?url=invalid");
    assert!(consume_link_file(&path, &dir.0).is_err());
    assert!(!path.exists());
    private_file(&path, &"x".repeat(8193));
    assert!(consume_link_file(&path, &dir.0).is_err());
    assert!(path.exists());
    let wrong = dir.0.join("some-other-file");
    private_file(&wrong, &raw);
    assert!(consume_link_file(&wrong, &dir.0).is_err());
    assert!(consume_link_file(&path, &dir.0.join("other")).is_err());
}

#[test]
#[cfg(unix)]
fn link_file_rejects_world_readable_files_and_symlinks() {
    use std::os::unix::fs::{PermissionsExt, symlink};
    let dir = Temp::new();
    let path = dir.0.join("agenterm-link.12345");
    let raw = link("https://example.com/c/ab1234", None);
    private_file(&path, &raw);
    fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
    assert!(consume_link_file(&path, &dir.0).is_err());
    fs::remove_file(&path).unwrap();
    let target = dir.0.join("target");
    private_file(&target, &raw);
    symlink(&target, &path).unwrap();
    assert!(consume_link_file(&path, &dir.0).is_err());
    assert_eq!(fs::read_to_string(target).unwrap(), raw);
}

#[test]
fn stable_handler_survives_source_removal_and_refuses_tampering() {
    let dir = Temp::new();
    let source = dir.0.join("temporary-download");
    private_file(&source, "client executable bytes");
    let destination = copy_executable(&source, &dir.0.join("installed")).unwrap();
    assert_eq!(
        copy_executable(&source, &dir.0.join("installed")).unwrap(),
        destination
    );
    fs::remove_file(&source).unwrap();
    assert_eq!(
        fs::read_to_string(&destination).unwrap(),
        "client executable bytes"
    );
    private_file(&source, "client executable bytes");
    fs::write(&destination, "modified executable").unwrap();
    assert!(copy_executable(&source, &dir.0.join("installed")).is_err());
}

#[test]
#[cfg(unix)]
fn stable_handler_rejects_a_symlink_at_the_content_addressed_path() {
    use std::os::unix::fs::symlink;
    let dir = Temp::new();
    let source = dir.0.join("download");
    private_file(&source, "client");
    let destination = copy_executable(&source, &dir.0.join("installed")).unwrap();
    fs::remove_file(&destination).unwrap();
    symlink(&source, &destination).unwrap();
    assert!(copy_executable(&source, &dir.0.join("installed")).is_err());
}

#[test]
#[cfg(target_os = "macos")]
fn mac_handler_script_compiles_and_bundle_signature_verifies() {
    use std::process::Command;
    let dir = Temp::new();
    let bundle = dir.0.join("Agenterm CLI.app");
    let script = macos::script(
        Path::new("/tmp/Agenterm's \"CLI\" $`\\\n中文"),
        Path::new("/tmp/Agenterm Inbox"),
    )
    .unwrap();
    let terminal = script
        .split("tell application \"Terminal\"")
        .nth(1)
        .unwrap();
    assert!(terminal.contains("-link-file"));
    assert!(!terminal.contains("incomingURL"));
    let output = Command::new("/usr/bin/osacompile")
        .arg("-o")
        .arg(&bundle)
        .arg("-e")
        .arg(script)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    macos::configure_bundle(&bundle).unwrap();
    assert!(
        Command::new("/usr/bin/codesign")
            .args(["--verify", "--deep", "--strict"])
            .arg(bundle)
            .status()
            .unwrap()
            .success()
    );
}

#[test]
fn linux_desktop_entry_quotes_executable_and_opens_a_terminal() {
    let entry = linux::desktop_entry(Path::new("/tmp/Agenterm CLI $%`\"\\file")).unwrap();
    assert!(entry.contains("Terminal=true\n"));
    assert!(entry.contains("MimeType=x-scheme-handler/agenterm-cli;\n"));
    assert!(
        entry.contains(r#"Exec="/tmp/Agenterm CLI \\$%%\\`\\"\\\\file" %u"#),
        "{entry}"
    );
    assert!(linux::desktop_entry(Path::new("/tmp/a=b")).is_err());
}

#[test]
#[cfg(unix)]
fn registration_permissions_are_safe_under_unusual_umasks() {
    use std::{os::unix::fs::PermissionsExt, process::Command};
    // Keep process-wide umask changes out of parallel tests and the user's shell.
    if std::env::var_os("AGENTERM_LINK_PERMISSION_PROBE").is_none() {
        let output = Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "deeplink::tests::registration_permissions_are_safe_under_unusual_umasks",
                "--nocapture",
            ])
            .env("AGENTERM_LINK_PERMISSION_PROBE", "1")
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}{}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        return;
    }
    unsafe { libc::umask(0) };
    let dir = Temp::new();
    let applications = dir.0.join("applications");
    let source = dir.0.join("download");
    private_file(&source, "client");
    linux::write_desktop(&applications, &source).unwrap();
    assert_eq!(
        fs::metadata(&applications).unwrap().permissions().mode() & 0o777,
        0o755
    );
    let desktop = applications.join("agenterm-cli.desktop");
    assert_eq!(
        fs::metadata(&desktop).unwrap().permissions().mode() & 0o777,
        0o644
    );
    fs::set_permissions(&desktop, fs::Permissions::from_mode(0o666)).unwrap();
    linux::write_desktop(&applications, &source).unwrap();
    assert_eq!(
        fs::metadata(&desktop).unwrap().permissions().mode() & 0o777,
        0o644
    );
    unsafe { libc::umask(0o177) };
    let copied = copy_executable(&source, &dir.0.join("installed")).unwrap();
    assert_eq!(
        fs::metadata(&copied).unwrap().permissions().mode() & 0o777,
        0o700
    );
    fs::set_permissions(&copied, fs::Permissions::from_mode(0o600)).unwrap();
    copy_executable(&source, &dir.0.join("installed")).unwrap();
    assert_eq!(
        fs::metadata(&copied).unwrap().permissions().mode() & 0o777,
        0o700
    );
}
