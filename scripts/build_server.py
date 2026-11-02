#!/usr/bin/env python3
"""Build a self-contained server with downloads prepared before startup."""
import argparse
import fcntl
import hashlib
import json
import lzma
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import time


CLIENTS = {
    f"agenterm-client-cli-{platform}-{arch}{'.exe' if platform == 'windows' else ''}"
    for platform in ("macos", "linux", "windows") for arch in ("amd64", "arm64")
}
DOWNLOADS = {"SHA256SUMS"} | {
    name.removesuffix(".exe") + (".zip" if name.endswith(".exe") else ".tar.gz")
    for name in CLIENTS
}
LINUX_CLI = "agenterm-client-cli-linux-amd64"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def native_env():
    return {key: value for key, value in os.environ.items()
            if key not in ("GOOS", "GOARCH", "GOARM", "GOAMD64", "CGO_ENABLED")}


def prepare(root, cache):
    cache.mkdir(parents=True, exist_ok=True)
    with (cache / '.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        return prepare_locked(root, cache)


def prepare_locked(root, cache):
    # Include the toolchain and packer sources: a version-only cache could serve
    # different archive bytes at the same public URL after a compressor change.
    sources = [root / "internal/installer/assets/clients.tar.xz",
               root / "cmd/agenterm-pack/main.go", root / "go.mod", root / "go.sum",
               root / "scripts/build_server.py"]
    sources += sorted((root / "internal/installer").glob("*.go"))
    toolchain = subprocess.check_output(["go", "version"], cwd=root, env=native_env())
    key = hashlib.sha256(toolchain + "".join(digest(p) for p in sources).encode()).hexdigest()
    cached = cache / key
    names = DOWNLOADS | {LINUX_CLI}
    try:
        hashes = json.loads((cached / "cache.json").read_text())
        hit = set(hashes) == names and all(digest(cached / n) == h for n, h in hashes.items())
    except (OSError, ValueError):
        hit = False
    if not hit:
        cache.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="prepare-", dir=cache) as directory:
            work = Path(directory)
            raw, packed = work / "raw", work / "packed"
            raw.mkdir()
            packed.mkdir()
            seen = set()
            with lzma.open(sources[0], "rb") as stream:
                with tarfile.open(fileobj=stream, mode="r|") as archive:
                    for entry in archive:
                        if entry.name not in CLIENTS or entry.name in seen or not entry.isfile() or not 0 < entry.size <= 64 << 20:
                            raise ValueError("Invalid or duplicate embedded CLI entry")
                        (raw / entry.name).write_bytes(archive.extractfile(entry).read())
                        seen.add(entry.name)
                # Tar EOF comes before the XZ footer; consume it to validate
                # the compressed stream's checksum before caching downloads.
                while stream.read(1 << 20):
                    pass
            if seen != CLIENTS:
                raise ValueError("Embedded bundle is missing CLI platforms")
            subprocess.run(["go", "run", "./cmd/agenterm-pack", "-input", str(raw),
                            "-output", str(packed), "-bundle", str(work / "clients.tar")],
                           cwd=root, env=native_env(), check=True)
            if {p.name for p in packed.iterdir()} != DOWNLOADS:
                raise ValueError("Incomplete prepared downloads")
            shutil.copy2(raw / LINUX_CLI, packed / LINUX_CLI)
            (packed / "cache.json").write_text(json.dumps({n: digest(packed / n) for n in sorted(names)}))
            # No partial cache is visible, including when packing fails.
            if cached.exists():
                shutil.rmtree(cached)
            packed.rename(cached)
    destination = root / "internal/installer/assets/downloads"
    destination.mkdir(exist_ok=True)
    if any(p.name not in DOWNLOADS for p in destination.iterdir()):
        raise ValueError("Unexpected file in generated download directory")
    for name in DOWNLOADS:
        shutil.copy2(cached / name, destination / name)
    return destination, cached / LINUX_CLI, hit


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--prepare-only", action="store_true")
    parser.add_argument("--linux-cli-output", type=Path)
    parser.add_argument("--cache-dir", type=Path, default=Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache"))) / "agenterm/downloads")
    parser.add_argument("--goos", default=os.environ.get("GOOS"))
    parser.add_argument("--goarch", default=os.environ.get("GOARCH"))
    args = parser.parse_args()
    if not args.prepare_only and not args.output:
        parser.error("--output is required unless --prepare-only is set")
    root = Path(__file__).resolve().parent.parent
    started = time.monotonic()
    downloads, cli, hit = prepare(root, args.cache_dir.resolve())
    prepared = time.monotonic()
    if args.linux_cli_output:
        shutil.copy2(cli, args.linux_cli_output)
        args.linux_cli_output.chmod(0o755)
    if not args.prepare_only:
        env = dict(os.environ, CGO_ENABLED="0")
        for key, value in (("GOOS", args.goos), ("GOARCH", args.goarch)):
            if value:
                env[key] = value
        subprocess.run(["go", "build", "-tags=agenterm_prebuilt", "-trimpath", "-gcflags=all=-l",
                        "-ldflags", "-s -w", "-o", str(args.output.resolve()), "./cmd/agenterm-server"],
                       cwd=root, env=env, check=True)
    print(json.dumps({"downloads": str(downloads), "cache_hit": hit,
                      "prepare_seconds": round(prepared - started, 3),
                      "build_seconds": round(time.monotonic() - prepared, 3)}), flush=True)


if __name__ == "__main__":
    main()
