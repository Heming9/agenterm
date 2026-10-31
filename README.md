# Agenterm

<img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm app icon" width="128">

English | [简体中文](README.zh-CN.md)

**Give your agent a shell on another machine. Over MCP.**

```text
Agent ── HTTP / MCP ──> Server <── WebSocket ── Client ──> shell
```

macOS / Linux / Windows · CLI / GUI · Multiple clients · Interactive sessions · No SSH daemon

## Quick start

Get binaries from [Releases](https://github.com/Heming9/agenterm/releases), or build with Go 1.24.3+:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

**1. Start the server.** On a trusted network:

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080 --generate
```

Replace `SERVER_IP` with an address reachable by both the agent and target machine. Copy the printed `agenterm:` token.

**2. Connect the target machine.** Run the client and paste the token:

```sh
./agenterm-client
```

Release servers also print one-command installers for macOS/Linux and Windows PowerShell. Share the appropriate command privately; the target user confirms before connecting.

**3. Connect your agent.** Add a remote MCP server with Streamable HTTP and custom headers:

```text
URL:            http://SERVER_IP:8080/mcp
Authorization:  Bearer agenterm:...
```

Use the same full token as the client. Then call `execute_command`:

```json
{"command":"pwd && ls"}
```

> A token grants shell access as the client’s user. Default HTTP/WS traffic is unencrypted; use a trusted network, or put an HTTPS reverse proxy in front of the server before exposing it publicly. [Security details](#details).

## MCP tools

| Tool | Use |
| --- | --- |
| `create_client` | Create a token and one-time invitation. Requires management access. |
| `wait_for_client` | Wait for the target to connect. |
| `execute_command` | Run a command; return output and exit code when done. |
| `session_start` | Start an interactive command; return `session_id`. |
| `session_read` | Read output with `session_id` and `cursor`; reuse `next_cursor`. |
| `session_write` | Send `input` to a session. Include a newline to submit a line. |
| `session_stop` | Stop a session. |

For multiple targets, connect MCP with the server’s printed **management key**. Call `create_client` with `{}`, show its `display_text` to the target user, then pass the returned `client_token` in subsequent tool calls:

```json
{"client_token":"agenterm:...","timeout_ms":30000}
```

Without `--generate`, the server creates connections only on demand. A client token controls one target; the management key controls all targets.

## Clients

- **CLI:** terminal UI by default. Arrow keys scroll, `c` clears the log, `q` / Ctrl+C disconnects. Use `-plain` for text output; redirected input or output selects it automatically.
- **GUI:** paste a token, connect, watch commands and live output. Follows the system theme; interface text is currently Chinese. [Build instructions below](#details).

Both clients keep recent logs in memory (~64 KiB), never save tokens, and require manual reconnection. Closing the client disconnects it.

## Details

<details>
<summary>Invitations & downloads</summary>

Release servers generate one-command installers that fetch the matching release’s script and CLI from GitHub, detect the architecture, and ask for authorization before connecting. No startup service is installed. The GUI is a separate download. Locally built servers do not provide download commands.

Invitations expire after 15 minutes and can be redeemed once. In the server console, Enter replaces unused startup invitations; existing connections and MCP-created invitations are unaffected. Redeemed client tokens remain reusable until the server restarts, with one connected client per token. Share credentials privately; invitation commands may remain in shell history.

The target needs access to GitHub and `-public-url`; `127.0.0.1` works only locally. Each invocation fetches the release checksum manifest and verifies the cached CLI, downloading it again if missing or damaged. Cache locations, partitioned by version, OS, and architecture:

| OS | Cache |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

The cache remains after disconnecting; delete it manually to remove it. Each connection still requires authorization. The installer script is fetched over HTTPS without a pinned hash. Release checksums detect corruption; they do not replace code signing or protect against a compromised release.

</details>

<details>
<summary>Authentication & deployment</summary>

The server defaults to `127.0.0.1:8080` and generates a new 10-character lowercase alphanumeric management key on each restart. Set a longer random key with `--passwd` for public deployments:

```sh
./agenterm-server -listen 127.0.0.1:8080 \
  -public-url https://agenterm.example.com \
  --passwd 'replace-with-a-long-random-key'
```

Configure an HTTPS reverse proxy with WebSocket support separately. `-public-url` advertises the address; it does not enable TLS. When the proxy is on the same machine, keep the backend bound to loopback. Public deployments also need appropriate access controls.

MCP accepts `Authorization: Bearer <management key>` or `Authorization: Bearer <client token>`. Management calls select a target with `client_token`; ordinary client tokens cannot call `create_client`. You can also configure one MCP entry per target. Your agent must support Streamable HTTP and custom headers.

`create_client` returns `client_token`, `invitation`, `expires_at`, and `display_text`. Release builds also return `macos_linux_command` and `windows_powershell_command`; both redeem the same invitation, so use one. The server allows up to 1000 client tokens. All tokens and invitations expire on restart, even with a fixed management key.

`-no-password` disables MCP management authentication: anyone who can reach the endpoint can create and control connections. Keep this mode private. It cannot be combined with `--passwd`.

Client tokens contain the server address and a 128-bit random credential. The current token format is incompatible with older versions; update server and client together.

</details>

<details>
<summary>Session behavior & platform limits</summary>

- `wait_for_client` returns `{"status":"connected"}` or `{"status":"timeout"}`. `timeout_ms`: default 30000, range 0–300000; 0 checks immediately. Show the invitation before waiting. The agent host must allow the chosen duration; call again after a timeout if needed.
- `session_read` polls for output. `wait_ms` waits up to 20000 ms; `done: true` includes `exit_code`. Each session retains the latest 1 MiB; lagging readers receive `truncated: true`. Finished sessions remain for at least 30 minutes and are cleaned up when a new session starts.
- macOS/Linux sessions use a PTY. Windows uses pipes: simple prompts work, programs requiring a real terminal do not. Remote graphical interfaces are unsupported.
- Full-screen terminal apps emit control sequences the agent may not understand. The CLI strips these from its local log; it is a log viewer. Input may be echoed, so avoid entering secrets through the agent.
- Commands use the supported shell that directly launched the client (PowerShell, cmd, bash, zsh, fish, etc.). Fallback: `/bin/sh` on macOS/Linux, `ComSpec` (usually `cmd.exe`) on Windows. Each command starts a fresh shell; launching-shell variables and functions are not shared.
- Windows output decoding detects UTF-8 or the system code page from the first non-ASCII content. Mixed encodings may still produce garbled text.
- After a disconnect, reconnect manually. After a server restart, obtain a new token.
- GUI statistics count completed commands, including failures and cancellations. Clearing the log affects only the display; a successful reconnection resets statistics. Connection attempts can be cancelled.

</details>

<details>
<summary>Security model</summary>

The server sees commands and output; the client displays them too. Secrets in either may be visible. Closing the client attempts to stop running commands, but cannot undo completed operations or guarantee that detached children stop.

HTTP and WebSocket are unencrypted by default, exposing credentials, commands, and output to anyone observing traffic. Use HTTPS across untrusted networks and keep tokens and management keys secret.

Authentication is rate-limited by directly connected source IP: 10 failures in one minute block the IP for five minutes, including valid credentials during the block. Shared-IP users affect each other. The server ignores forwarded headers; behind a proxy, also rate-limit by real source IP at the proxy. Distributed sources can still attempt authentication.

</details>

<details>
<summary>Cross-compilation & GUI packaging</summary>

CLI cross-compilation:

```sh
GOOS=linux GOARCH=amd64 go build -o agenterm-client-linux-amd64 ./cmd/agenterm-client
GOOS=windows GOARCH=amd64 go build -o agenterm-client-windows-amd64.exe ./cmd/agenterm-client
```

Build the Fyne GUI with CGo and a C compiler. Linux also needs graphics development libraries; cross-compilation needs a target C toolchain. See [Fyne setup](https://docs.fyne.io/started/).

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
# Windows: hide the console window
go build -ldflags=-H=windowsgui -o Agenterm.exe ./cmd/agenterm-gui
```

For an application bundle with its icon, install the matching Fyne CLI and run the command for your target OS on that platform:

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
cd cmd/agenterm-gui
fyne package -os darwin --use-raw-icon   # Agenterm.app
fyne package -os windows -icon assets/app-icon-windows.png  # Agenterm.exe
```

Windows packaging requires a working Windows C compiler. Packaging increments the build number in `FyneApp.toml`.

</details>

<details>
<summary>Probe MCP with curl</summary>

```sh
curl -s http://127.0.0.1:8080/mcp \
  -H 'Authorization: Bearer YOUR_AGENTERM_TOKEN' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_command","arguments":{"command":"echo hello"}}}'
```

</details>

## Test

```sh
go test ./...
```

## License

[Apache-2.0](LICENSE)
