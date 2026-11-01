clients.tar.xz is a tracked default asset, included in ordinary server builds.
It contains all six raw CLI executables, ordered by architecture and compressed
together with XZ. Do not also embed the individual download archives.

After changing CLI/shared client code, run from the repository root:

    go generate ./internal/installer

This needs Python 3 and Go, rebuilds all six CLI targets, and atomically replaces
the bundle. Include the updated bundle with source changes. The release workflow
also refreshes it before building servers. No release linker flag is required.

The server's pure Go decoder prepares standard gzip/ZIP downloads and SHA256SUMS
in memory at startup, before listening. cmd/agenterm-pack uses the same archive
writer and Go toolchain to produce identical release files. CI verifies all
reconstructed files against the release directory before building the servers.
