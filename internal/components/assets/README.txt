components.tar.xz is the single tracked archive embedded by every Server build.

Contents (one shared XZ stream, not nested compressed downloads):
    manifest.json       Schema 2: sizes/SHA-256 of raw executables and P2P version
    client-cli/         Six raw CLI executables (Windows names end in .exe)
    p2p-cli/            Six raw P2P executables with OS/architecture names

Regenerate CLI binaries while retaining existing native P2P binaries:
    go generate ./internal/components

Update P2P from all six verified native build outputs and rebuild all six CLIs:
    python3 scripts/build_components.py --p2p-assets build-macos-* build-windows-* build-linux-*

Packing rejects distributions missing any OS/architecture combination. The
Python builder applies strong XZ compression to the solid bundle at build time.
Native build outputs are raw binaries and a version/checksum manifest.

Only agenterm-server needs deployment. Startup validates and atomically extracts
raw executables under components/<bundle-sha256>/, then writes the raw CLI checksum
list and P2P source manifest. No platform archives are generated at startup.
Valid caches are verified and reused. Old frontends retain immutable directories.
The server embeds a Go XZ decoder, independent of installed compression programs.

Distribution packs only the requested OS/architecture/format. The frontend shares
concurrent packing work and caches derived bytes in bounded memory (128 MiB per
component). It does not write derived archives into the raw component directory.
CLI/P2P endpoints offer tar.xz, Unix tar.gz, Windows ZIP, and the raw executable.
Each downloadable filename also supports a .sha256 endpoint. Installers verify
archive downloads and the final executable against its source SHA-256.

Installers probe an embedded tiny XZ archive with the target's tar implementation.
macOS/Linux select tar.xz, then tar.gz, then the raw binary. Windows selects
XZ-capable tar.exe, then Expand-Archive/ZIP, then the raw .exe. No decompressor
needs to be installed. Cache validation always uses the executable's source hash.

A release with existing CLI binaries can use --client-dir and --download-output.
The Go packer and HTTP download path share the deterministic per-file encoders.
GitHub Release publishes only original executables and version metadata.
HTTP distribution generates archives only on demand. scripts/build_server.py prepares raw sources for local build/deploy
helpers without generating all download formats.
