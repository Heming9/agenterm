components.zip is the single tracked archive embedded by every Server build.

Contents:
    manifest.json       SHA-256 and byte size for every payload file
    client-cli/         Six exact CLI archives and SHA256SUMS
    p2p-cli/            Six P2P archives and their version/checksum manifest

Regenerate CLI downloads while retaining the existing native P2P archives:
    go generate ./internal/components

Update P2P from all six verified build outputs and rebuild all six CLIs:
    python3 scripts/build_components.py --p2p-assets build-macos-* build-windows-* build-linux-*

Packing rejects distributions missing any OS/architecture combination.

A release with existing CLI binaries can use --client-dir and --download-output.
The Go packer owns the deterministic download encoding and the unified ZIP.
Already-compressed payloads use ZIP Store, preserving exact published bytes.

Only agenterm-server needs deployment. Startup validates all entries and extracts
atomically into the instance runtime directory: components/<bundle-sha256>/.
Valid caches are verified and reused. Old frontends retain their own immutable
bundle directory during reloads. No startup archive recompression is performed.
