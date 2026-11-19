use std::{path::Path, process::Command};

fn git(args: &[&str]) -> Option<String> {
    let output = Command::new("git").args(args).output().ok()?;
    if !output.status.success() {
        return None;
    }
    Some(String::from_utf8(output.stdout).ok()?.trim().to_owned())
}

fn build_revision() -> String {
    let mut refs = vec!["HEAD".to_owned(), "packed-refs".to_owned()];
    refs.extend(git(&["symbolic-ref", "-q", "HEAD"]));
    for reference in refs {
        if let Some(path) = git(&["rev-parse", "--git-path", &reference])
            && Path::new(&path).is_file()
        {
            println!("cargo:rerun-if-changed={path}");
        }
    }
    git(&["rev-parse", "--short=7", "HEAD"]).unwrap_or_else(|| "unknown".into())
}

fn write_colors(out: &Path) {
    let path = "../../internal/theme/theme.json";
    println!("cargo:rerun-if-changed={path}");
    let palette: serde_json::Value =
        serde_json::from_slice(&std::fs::read(path).expect("read theme")).expect("parse theme");
    let mut source = String::new();
    let logo: [String; 3] = serde_json::from_value(palette["terminal-logo"].clone())
        .expect("theme must define three terminal logo rows");
    source.push_str(&format!("pub const TERMINAL_LOGO: [&str; 3] = {logo:?};\n"));
    for role in [
        "primary",
        "secondary",
        "success",
        "warning",
        "error",
        "selection",
        "on-selection",
    ] {
        let color = palette["terminal"][role]
            .as_str()
            .and_then(|hex| hex.strip_prefix('#'))
            .filter(|hex| hex.len() == 6)
            .and_then(|hex| u32::from_str_radix(hex, 16).ok())
            .unwrap_or_else(|| panic!("invalid theme color: terminal.{role}"));
        let name = role.to_ascii_uppercase().replace('-', "_");
        source.push_str(&format!("pub const {name}: u32 = 0x{color:06x};\n"));
    }
    std::fs::write(out.join("theme_colors.rs"), source).expect("write theme");
}

fn main() {
    let path = "../../internal/version/client-wire.txt";
    println!("cargo:rerun-if-changed={path}");
    let text = std::fs::read_to_string(path).expect("read Agenterm protocol version");
    println!("cargo:rustc-env=AGENTERM_WIRE_VERSION={}", text.trim());
    println!("cargo:rustc-env=AGENTERM_COMMIT={}", build_revision());

    // Share UI wording with the server and installers without embedding
    // the server's entire translation catalog in the executable.
    let catalog = "../../internal/i18n/messages.json";
    println!("cargo:rerun-if-changed={catalog}");
    let messages: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(catalog).expect("read translations"))
            .expect("parse translations");
    let keys = [
        "select.history.server",
        "history.server.empty",
        "history.server.failed",
        "history.server.select.help",
        "paste.connection.token",
        "disconnected.2",
        "connecting",
        "connected.temporary",
        "connected.persistent",
        "connection.mode.temporary",
        "connection.mode.persistent",
        "connection.mode.select",
        "connection.mode.help",
        "3.4.connecting",
        "connection.token",
        "enter.connect.ctrl.c.quit",
        "tui.ctrl.c.quit",
        "tui.command.count",
        "tui.connection.connecting",
        "tui.connection.connected",
        "tui.connection.token_validated",
        "tui.connection.server_version",
        "tui.connection.unknown_version",
        "tui.connection.elapsed",
        "tui.connection.handshake",
        "tui.connection.total_elapsed",
        "tui.welcome.greeting",
        "tui.welcome.waiting",
        "tui.selection.help",
        "tui.selection.copied",
        "tui.selection.copy.failed",
        "tui.selection.sent",
        "tui.fold.lines",
        "remote.control.authorization.prompt",
        "connection.retry.status",
        "connection.restored",
        "installer.consent.menu",
        "installer.consent.no",
        "installer.consent.temporary",
        "installer.consent.persistent",
        "temporary.assistance.requires.installer",
        "invalid.agenterm.connection.address.or.token",
    ];
    let mut source = String::from("pub fn tr(key: &str) -> &'static str { match key {\n");
    for key in keys {
        let en = messages[key]["en"].as_str().expect(key);
        let zh = messages[key]["zh-CN"].as_str().expect(key);
        source.push_str(&format!("{key:?} => text({en:?}, {zh:?}),\n"));
    }
    source.push_str("_ => panic!(\"unknown UI translation: {key}\"),\n} }\n");
    let out = std::path::PathBuf::from(std::env::var_os("OUT_DIR").unwrap());
    std::fs::write(out.join("ui_messages.rs"), source).expect("write translations");
    write_colors(&out);
}
