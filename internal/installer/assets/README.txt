clients.tar.xz is a tracked default asset, included in ordinary server builds.
It contains all six raw CLI executables, ordered by architecture and compressed
together with XZ.

After changing CLI/shared client code, run from the repository root:

    go generate ./internal/installer

This needs Python 3 and Go, rebuilds all six CLI targets, and atomically replaces
the bundle. Include the updated bundle with source changes. The release workflow
also refreshes it before building servers. No release linker flag is required.

The server's pure Go decoder prepares standard gzip/ZIP downloads and SHA256SUMS
in memory at startup, before listening. cmd/agenterm-pack uses the same archive
writer and Go toolchain to produce identical release files. CI verifies all
reconstructed files against the release directory before building the servers.

For deployments that need fast frontend replacement, use:

    python3 scripts/build_server.py --output dist/agenterm-server --goos linux --goarch amd64

This prepares the identical six archives and SHA256SUMS at build time, then
builds with the agenterm_prebuilt tag. That build embeds only assets/downloads/*,
not clients.tar.xz, and serves them directly without startup decompression or
recompression. Generated downloads are ignored by Git. The executable is larger
because archives are compressed separately rather than as one solid XZ stream.

Prepared downloads are cached by bundle contents, packer sources and Go version
under ~/.cache/agenterm/downloads (or XDG_CACHE_HOME). Cache hashes are checked
before reuse; changes invalidate the cache. Use --prepare-only to prepare assets
before a custom go build -tags=agenterm_prebuilt. Plain go build still uses the
tracked bundle and needs no preparation step.
