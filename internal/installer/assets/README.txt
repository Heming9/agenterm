The release workflow places clients.tar.xz here before building the server.
It contains all six raw CLI executables, ordered by architecture and compressed
together with xz -9e -T1. Do not also embed the individual download archives.

The server's pure Go decoder prepares standard gzip/ZIP downloads and SHA256SUMS
in memory at startup, before listening. cmd/agenterm-pack uses the same archive
writer and Go toolchain to produce identical release files. CI verifies all
reconstructed files against the release directory before building the servers.
