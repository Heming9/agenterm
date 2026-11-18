#!/usr/bin/env python3
"""Build/test Linux ARM64 and x64 locally in an ARM64 Docker environment."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=("arm64", "amd64", "all"), default="all")
    parser.add_argument("--image", default="agenterm-cli-cross:rust-1.99.1")
    parser.add_argument("--build-image", action="store_true")
    parser.add_argument("--test", action="store_true", help="Run unit and full Go server/MCP interoperability tests")
    args = parser.parse_args()
    component = Path(__file__).resolve().parent
    root = component.parent.parent
    relative = component.relative_to(root)
    if args.build_image:
        subprocess.run(["docker", "build", "--platform", "linux/arm64", "-t", args.image, "-"],
                       input=(component / "Dockerfile.cross").read_bytes(), check=True)
    architecture = subprocess.check_output(["docker", "run", "--rm", args.image, "uname", "-m"], text=True).strip()
    if architecture != "aarch64":
        parser.error("this helper expects the ARM64 Docker image; rebuild with --build-image")
    cargo_home = Path(os.environ.get("CARGO_HOME", str(Path.home() / ".cargo")))
    mounts = ["-v", f"{root}:/work", "-v", f"{cargo_home / 'registry'}:/usr/local/cargo/registry"]
    if (cargo_home / "config.toml").is_file():
        mounts += ["-v", f"{cargo_home / 'config.toml'}:/usr/local/cargo/config.toml:ro"]
    native = component / "target/cross/linux-arm64"
    native.mkdir(parents=True, exist_ok=True)
    if args.test:
        # Build the test harness and Go server on the host; only the Rust
        # executable needs to be emulated for the x64 interoperability run.
        env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
        subprocess.run(["go", "test", "-c", "-o", str(native / "interop.test"), "./components/agenterm-cli/tests"], cwd=root, env=env, check=True)
        subprocess.run(["go", "build", "-trimpath", "-gcflags=all=-l", "-ldflags", "-s -w", "-o", str(native / "agenterm-server"), "./cmd/agenterm-server"], cwd=root, env=env, check=True)
    for arch in ("arm64", "amd64") if args.arch == "all" else (args.arch,):
        target = {"arm64": "aarch64-unknown-linux-gnu", "amd64": "x86_64-unknown-linux-gnu"}[arch]
        output = component / f"target/cross/linux-{arch}"
        output.mkdir(parents=True, exist_ok=True)
        cargo_target = f"/work/{relative}/target/linux-{arch}-build"
        release = f"{cargo_target}/{target}/release/agenterm-cli"
        executable = f"/work/{relative}/target/cross/linux-{arch}/agenterm-cli"
        env = {"CARGO_TARGET_DIR": cargo_target, "CARGO_BUILD_JOBS": "4"}
        if arch == "amd64":
            env.update(PKG_CONFIG_ALLOW_CROSS="1", PKG_CONFIG_LIBDIR="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/share/pkgconfig",
                       CC_x86_64_unknown_linux_gnu="x86_64-linux-gnu-gcc", CFLAGS_x86_64_unknown_linux_gnu="-I/usr/include/x86_64-linux-gnu",
                       CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_RUNNER="qemu-x86_64")
            wrapper = output / "run.sh"
            wrapper.write_text(f'#!/bin/sh\nexec qemu-x86_64 {shlex.quote(executable)} "$@"\n')
            wrapper.chmod(0o755)
            runner = f"/work/{relative}/target/cross/linux-amd64/run.sh"
        else:
            runner = executable
        env["AGENTERM_RUST_CLI"] = runner
        env["AGENTERM_GO_SERVER"] = f"/work/{relative}/target/cross/linux-arm64/agenterm-server"
        common = f"--release --locked --offline --target {target} --manifest-path {relative}/Cargo.toml"
        commands = []
        if args.test:
            commands.append(f"cargo test {common}")
        commands += [f"cargo build {common}", f"cp {shlex.quote(release)} {shlex.quote(executable)}"]
        if args.test:
            commands.append(f"/work/{relative}/target/cross/linux-arm64/interop.test -test.v")
        commands.append(f"readelf -d {shlex.quote(executable)}")
        # The detached server runtime must be reaped when it shuts down.
        command = ["docker", "run", "--init", "--rm", *mounts]
        for key, value in env.items():
            command += ["-e", f"{key}={value}"]
        subprocess.run([*command, args.image, "sh", "-c", " && ".join(commands)], cwd=root, check=True)
        size = (output / "agenterm-cli").stat().st_size
        print(json.dumps({"arch": arch, "bytes": size}))


if __name__ == "__main__":
    main()
