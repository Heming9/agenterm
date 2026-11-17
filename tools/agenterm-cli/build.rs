fn main() {
    let path = "../../internal/version/version.go";
    println!("cargo:rerun-if-changed={path}");
    let text = std::fs::read_to_string(path).expect("read Agenterm protocol version");
    let version = text
        .split("const Current = \"")
        .nth(1)
        .and_then(|s| s.split('"').next())
        .expect("version constant");
    println!("cargo:rustc-env=AGENTERM_VERSION={version}");

    // Keep the Rust UI's wording identical to the Go client without embedding
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
        "tui.selection.help",
        "tui.selection.copied",
        "tui.selection.copy.failed",
        "tui.selection.sent",
        "tui.fold.lines",
        "4.4.connected",
        "remote.control.authorization.prompt",
        "connection.retry",
        "connection.retry.status",
        "connection.restored",
        "installer.consent.menu",
        "installer.consent.no",
        "installer.consent.temporary",
        "installer.consent.persistent",
        "installer.consent.number",
        "connection.idle.disconnected",
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
}
