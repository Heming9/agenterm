# Rust CLI

The maintained Agenterm client, with a Ratatui / Crossterm interface. Release
builds and the server's embedded client downloads use this Rust implementation.
Networking and shell management use threads, with no asynchronous runtime.

## Build and run

Requires a current stable Rust toolchain (tested with Rust 1.99.1). `build.rs`
reads the wire version from `internal/version/version.go` and compiles the UI's
translations from `internal/i18n/messages.json`, so build inside this repository.
Dependencies are pinned in `Cargo.lock`.

```sh
cargo build --release --locked --manifest-path tools/agenterm-cli/Cargo.toml
tools/agenterm-cli/target/release/agenterm-cli
```

The interactive prompt accepts a full device token, a short-code URL, or a bare
six-character short code when a server is already in `~/.agenterm/servers.json`.
Paste short-code URLs as issued. An address without a scheme uses HTTP.
For HTTPS, include the `https://` prefix.
Existing server history and per-server connection locks are shared.

```sh
tools/agenterm-cli/target/release/agenterm-cli -invite INVITATION
tools/agenterm-cli/target/release/agenterm-cli -token TOKEN -connection-mode persistent
tools/agenterm-cli/target/release/agenterm-cli -token https://SERVER/agenterm/c/ABC123
```

## Protocol links

Ordinary launches automatically register the current user's `agenterm-cli:`
protocol handler. Browser links use the same terminal-client format as before:

```text
agenterm-cli://connect?url=https%3A%2F%2Fexample.com%2Fagenterm%2Fc%2FABC123&mode=persistent
```

Only an HTTP(S) short-code address and the optional `mode=temporary` or
`mode=persistent` are accepted. The connection opens directly in the TUI using
that mode; omitting the mode defaults to temporary. The invitation page's mode
selection is preserved. These links follow `-token` behavior; `-invite` continues
to show its authorization prompt. Unsupported/duplicate parameters and malformed
links are rejected before a connection starts. The former GUI's `agenterm:`
scheme is not claimed by this terminal client.

The handler keeps a checksum-verified, content-addressed executable under the
user's configuration directory, so deleting the original download or installer
cache does not break browser launch. macOS installs `Agenterm CLI.app`, signs and
registers it with Launch Services, and opens Terminal using a private, one-use
`-link-file` rather than putting the link in shell history. Windows registers
`HKCU\Software\Classes\agenterm-cli`; Linux installs a `Terminal=true` desktop
entry and associates it using `xdg-mime`. Registration failure is reported as a
warning and does not prevent ordinary terminal connections. Protocol launches
skip registration to avoid replacing an active handler.

## Interactive interface

Invitations show an authorization explanation and temporary/persistent/deny
choices, using arrow keys on a terminal and numbered input when
redirected. Temporary connections disconnect
after five minutes of inactivity, excluding time spent running commands. The
`-consent-confirmed` installer flag requires `-invite`. The interactive interface
is the default when both stdin and stdout are terminals; `-plain` or redirected
input/output selects plain output. Ctrl+C disconnects and stops owned shells.

`AGENTERM_LANG=zh-CN` selects Chinese (`LANG` / `LC_ALL` are also recognized).
Authorization text and connection menus share translations with the server. The TUI
has a header, dividers, input form, connection mode and server history
selectors, status colors, command counter and latency footer. It adapts to window
size and terminal background; `AGENTERM_THEME=light` / `dark` overrides background
detection. `NO_COLOR` and `TERM=dumb` disable colors while retaining visible menu
and text-selection highlights.

Commands and output appear in a bounded 10 MiB log with timestamps, spacing,
wrapping and initial multiline folding. Click a folded block to expand/collapse;
use arrows, PageUp/PageDown, Home/End or the mouse wheel to scroll. Drag to select
and copy, including across wrapped lines; dragging beyond the log scrolls the
selection. Esc clears the selection, Ctrl+C retries copying while a selection is
active, otherwise it disconnects and returns to the connection form. On that
form Ctrl+C clears the token first, then quits. Clipboard writes use platform
commands locally and OSC 52 over SSH (including tmux/screen). Plain mode retains
natural terminal scrollback and command completion footers.

## Terminal sessions and transfers

The client owns shell processes and retained results. See the [terminal client API](../../docs/client-api.md)
for the WebSocket contract and the [MCP tool reference](../../docs/mcp-tools.md) for session controls and limits.
Unix uses PTYs. Windows uses PowerShell with pipes and a kill-on-close Job Object.
For file transfers the CLI runs a verified [P2P helper](../p2p-cli/README.md) in the foreground
and stops it when the WebSocket connection ends.

## Networking and portability

The client uses Rust HTTP/WebSocket framing (`httparse` / `tungstenite`)
and uses **platform TLS** through `native-tls`: Security.framework on macOS,
Schannel on Windows, dynamically linked OpenSSL on Linux. It does not use
URLSession or WinHTTP. Certificate and hostname validation stay enabled.

macOS and Windows use system components. Linux builds require OpenSSL development
headers and `pkg-config` (e.g. `libssl-dev pkg-config` on Debian/Ubuntu); the target
machine needs the matching OpenSSL shared libraries. This is not a fully static,
dependency-free Linux executable. Build Linux binaries on the intended baseline
distribution. A Linux/macOS/Windows artifact is built separately for each CPU.
Synchronous DNS and TLS handshakes rely on OS and socket timeouts. Cancellation
takes effect after a blocking call returns.

`AGENTERM_CA_FILE=/path/to/ca.pem` adds an explicit private CA without changing
the system trust store. `AGENTERM_POWERSHELL` can select a PowerShell executable
on Windows. Unix supports sh/bash/zsh and otherwise uses `/bin/sh`.

Not yet supported: elevation/relaunch, proxy configuration,
fish integration, or Windows legacy-code-page output decoding. Errors use English
when a localized message is unavailable.

## Verification

```sh
cargo fmt --check --manifest-path tools/agenterm-cli/Cargo.toml
cargo clippy --locked --manifest-path tools/agenterm-cli/Cargo.toml --all-targets -- -D warnings
cargo test --locked --manifest-path tools/agenterm-cli/Cargo.toml
AGENTERM_LANG=en cargo test --locked --manifest-path tools/agenterm-cli/Cargo.toml tui::tests
AGENTERM_LANG=zh-CN cargo test --locked --manifest-path tools/agenterm-cli/Cargo.toml tui::tests
cargo build --release --locked --manifest-path tools/agenterm-cli/Cargo.toml

# Real Go token encoder + HTTP/WebSocket/TLS server fixtures.
AGENTERM_RUST_CLI="$PWD/tools/agenterm-cli/target/release/agenterm-cli" \
  go test -v ./tools/agenterm-cli/tests

# Full isolated Go server/runtime + official MCP SDK + Rust client.
go build -trimpath -gcflags=all=-l -ldflags='-s -w' \
  -o tools/agenterm-cli/target/agenterm-server ./cmd/agenterm-server
AGENTERM_RUST_CLI="$PWD/tools/agenterm-cli/target/release/agenterm-cli" \
AGENTERM_GO_SERVER="$PWD/tools/agenterm-cli/target/agenterm-server" \
  go test -v ./tools/agenterm-cli/tests

python3 tools/agenterm-cli/measure.py

# macOS only, opt-in: registers the real handler and opens a Terminal window.
AGENTERM_RUST_CLI="$PWD/tools/agenterm-cli/target/release/agenterm-cli" \
AGENTERM_TEST_NATIVE_LINK=1 \
  go test -v ./tools/agenterm-cli/tests -run '^TestRustNativeMacProtocolLaunch$'
```

The standard tests use local servers, temporary homes, a test CA and isolated protocol
registration. The opt-in macOS native link test registers a handler and opens Terminal.
The [CI matrix](../../.github/workflows/checks.yml) defines the current native test coverage.
[verification.json](verification.json) records prototype measurements for the original artifacts.

## Release builds

The [release workflow](../../.github/workflows/release.yml) uses native runners for all six
OS and architecture combinations. Linux releases build on Ubuntu 24.04 and require
compatible glibc and OpenSSL shared libraries. Windows releases link the CRT statically.
See the [component distribution guide](../../internal/components/assets/README.txt) for
build commands, filenames and the embedded server bundle.

## Local cross builds

On an ARM64 Mac, build the Windows x64 client using Rust's LLD linker and the
x64 MSVC CRT and Windows SDK `.lib` files. Keep the SDK in an ignored build directory.

```sh
rustup target add x86_64-pc-windows-msvc
python3 tools/agenterm-cli/build_windows.py \
  --sdk-lib tools/agenterm-cli/target/cross/windows-sdk --tests
```

The SDK directory must contain the combined libraries from MSVC `lib/x64`,
Windows SDK `ucrt/x64`, and SDK `um/x64`. The executable is written to
`target/cross/windows-amd64/agenterm-cli.exe`. The Windows test machine only
needs the compiled executables to run tests, not a Rust installation.

Linux builds and runtime tests use an ARM64 Docker image with an x64 cross
compiler, both OpenSSL development packages, a target glibc sysroot, and QEMU:

```sh
cargo fetch --locked --manifest-path tools/agenterm-cli/Cargo.toml
python3 tools/agenterm-cli/build_linux.py --build-image --test
```

The helper shares the local Cargo registry cache and builds offline. It writes
`target/cross/linux-arm64/agenterm-cli` and `target/cross/linux-amd64/agenterm-cli`.
Subsequent runs can omit `--build-image`; `--arch arm64` or `--arch amd64`
selects one architecture. All paths above are relative to `tools/agenterm-cli`
unless the full repository path is shown. Build products and SDKs are ignored.
