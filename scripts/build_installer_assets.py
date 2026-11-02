#!/usr/bin/env python3
"""Regenerate the tracked six-platform client bundle for ordinary server builds.

Run `go generate ./internal/installer` after changing CLI/shared client code.
Only regeneration needs Python and Go; server builds need no extra tools/flags.
"""

import concurrent.futures
import lzma
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    root = Path(__file__).resolve().parent.parent
    bundle = root / "internal/installer/assets/clients.tar.xz"
    bundle.parent.mkdir(parents=True, exist_ok=True)
    # Bootstrap if the file was deleted: agenterm-pack imports installer, whose
    # embed pattern must resolve, but does not read the old bundle at runtime.
    bootstrap = not bundle.exists()
    if bootstrap:
        bundle.touch()
    try:
        with tempfile.TemporaryDirectory(prefix="agenterm-installers-") as directory:
            work = Path(directory)

            def build(target):
                goos, arch = target
                platform = "macos" if goos == "darwin" else goos
                suffix = ".exe" if goos == "windows" else ""
                name = f"agenterm-client-cli-{platform}-{arch}{suffix}"
                print(f"Building {name}", flush=True)
                env = dict(os.environ, GOOS=goos, GOARCH=arch, CGO_ENABLED="0")
                subprocess.run(["go", "build", "-trimpath", "-gcflags=all=-l", "-ldflags", "-s -w",
                                "-o", str(work / name), "./cmd/agenterm-client-cli"], cwd=root, env=env, check=True)

            targets = [(goos, arch) for goos in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")]
            with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
                list(executor.map(build, targets))
            raw = work / "clients.tar"
            # The packer must run natively even when go generate was invoked in
            # a cross-compilation environment.
            pack_env = {k: v for k, v in os.environ.items() if k not in ("GOOS", "GOARCH", "GOARM", "GOAMD64")}
            subprocess.run(["go", "run", "./cmd/agenterm-pack", "-input", str(work), "-output", str(work),
                            "-bundle", str(raw)], cwd=root, env=pack_env, check=True)
            print("Compressing the six-platform installer bundle", flush=True)
            # Write beside the destination for an atomic replacement. Failed
            # regeneration leaves the existing bundle available for builds.
            temporary = bundle.with_suffix(".xz.tmp")
            try:
                with raw.open("rb") as source, lzma.open(temporary, "wb", preset=9 | lzma.PRESET_EXTREME) as output:
                    shutil.copyfileobj(source, output)
                temporary.replace(bundle)
            finally:
                temporary.unlink(missing_ok=True)
            print(f"Updated {bundle.relative_to(root)} ({bundle.stat().st_size:,} bytes)", flush=True)
    finally:
        if bootstrap and bundle.exists() and bundle.stat().st_size == 0:
            bundle.unlink()


if __name__ == "__main__":
    main()
