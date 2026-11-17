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
}
