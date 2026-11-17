#!/usr/bin/env python3
"""Cross-link an x64 Windows release using Rust's LLD and an existing MSVC SDK."""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sdk-lib", required=True, type=Path,
                        help="Directory containing MSVC CRT and Windows SDK x64 .lib files")
    parser.add_argument("--tests", action="store_true", help="Also cross-compile the unit-test executable")
    args = parser.parse_args()
    component = Path(__file__).resolve().parent
    sdk = args.sdk_lib.resolve()
    required = ("libcmt.lib", "libvcruntime.lib", "libucrt.lib", "kernel32.lib", "uuid.lib")
    for name in required:
        if not (sdk / name).is_file():
            parser.error(f"Missing x64 SDK library: {sdk / name}")
    sysroot = Path(subprocess.check_output(["rustc", "--print", "sysroot"], text=True).strip())
    host = next(line[6:] for line in subprocess.check_output(["rustc", "-vV"], text=True).splitlines()
                if line.startswith("host: "))
    linker = sysroot / "lib/rustlib" / host / "bin" / ("rust-lld.exe" if os.name == "nt" else "rust-lld")
    if not linker.is_file():
        parser.error(f"Rust LLD linker not found: {linker}")
    env = os.environ.copy()
    flags = (env["CARGO_ENCODED_RUSTFLAGS"].split("\x1f")
             if env.get("CARGO_ENCODED_RUSTFLAGS") else shlex.split(env.get("RUSTFLAGS", "")))
    flags += ["-C", "target-feature=+crt-static", "-L", f"native={sdk}", "-C", "link-arg=/DEBUG:NONE"]
    env["CARGO_ENCODED_RUSTFLAGS"] = "\x1f".join(flags)
    env["CARGO_TARGET_X86_64_PC_WINDOWS_MSVC_LINKER"] = str(linker)
    common = ["--locked", "--release", "--target", "x86_64-pc-windows-msvc", "--manifest-path", str(component / "Cargo.toml")]
    subprocess.run(["cargo", "build", *common], env=env, check=True)
    if args.tests:
        subprocess.run(["cargo", "test", "--no-run", *common], env=env, check=True)
    metadata = json.loads(subprocess.check_output([
        "cargo", "metadata", "--no-deps", "--format-version", "1", "--manifest-path", str(component / "Cargo.toml")], text=True))
    source = Path(metadata["target_directory"]) / "x86_64-pc-windows-msvc/release/agenterm-cli.exe"
    destination = component / "target/cross/windows-amd64/agenterm-cli.exe"
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)
    print(json.dumps({"file": str(destination), "bytes": destination.stat().st_size}))


if __name__ == "__main__":
    main()
