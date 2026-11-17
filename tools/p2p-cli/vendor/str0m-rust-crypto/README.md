# str0m RustCrypto provider

This is the RustCrypto provider from str0m 0.24.1, published separately as
`str0m-rust-crypto` 0.6.0. The source is from upstream commit
`9b159720be773693dd561dec0fb20c6d7e4aa323` under `crypto/rust-crypto`,
used under the included MIT license.

The local patch uses RustCrypto P-256 to generate and sign the DTLS certificate
through rcgen's signing API. This allows dimpl to build with the `rust-crypto`
feature alone. Remove the patch when the upstream provider supports
certificate generation without AWS-LC.
