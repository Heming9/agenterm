<div align="center">
  <h1>Agenterm</h1>
  <img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm app icon" width="128">
  <p>English | <a href="README.zh-CN.md">简体中文</a></p>
  <p><strong>Let agents control any remote computer, anytime, anywhere.</strong></p>
  <p>Connect a remote computer with a single command for AI agent remote assistance through its terminal using MCP.</p>
  <p><code>Agent ── HTTP / MCP ──&gt; Server &lt;── WebSocket ── Client ──&gt; shell</code></p>
  <p>macOS / Linux / Windows · CLI / GUI · Multiple clients · Interactive sessions · No SSH daemon</p>
</div>

## Use the hosted service

Connect an agent that supports Streamable HTTP to the hosted Server; no Server deployment is needed:

```text
https://agenterm.net/mcp
```

The hosted management endpoint currently does not require authentication. For example, add this to `~/.codex/config.toml` for Codex:

```toml
[mcp_servers.agenterm]
url = "https://agenterm.net/mcp"
```

Restart the agent or reload its MCP configuration, then:

1. Call `create_client` with `{}`. **Privately share** the one-command installer for the target's OS from the returned `display_text` with the person using that computer.
2. The recipient runs the command on their computer, reads the prompt, and authorizes remote control. The macOS/Linux and Windows commands share one invitation: use either one. Invitations expire after 15 minutes and can be redeemed once.
3. Call `wait_for_client` with the returned `client_token`. Once it reports `connected`, pass that same `client_token` to `execute_command` or a session tool to select the target, for example:

   ```json
   {"client_token":"agenterm:...","command":"pwd && ls"}
   ```

The hosted service allows up to four connected clients per **IP address issuing invitations**; users sharing a public IP share that quota. When capacity is available, creating another invitation may invalidate the oldest unused one. Server restarts invalidate existing tokens and invitations; create new ones afterward.

> The hosted management endpoint is public: anyone who can reach it can create their own clients, and anyone holding a target's client token can control that computer. Keep tokens and invitations private and connect only with the other person's authorization. To control management access yourself, use the self-hosted setup below.

## Self-hosted quick start

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
URL:            http://SERVER_IP:8080/agenterm/mcp
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
{"client_token":"agenterm:...","timeout_ms":90000}
```

Without `--generate`, the server creates connections only on demand. A client token controls one target; the management key controls all targets.

## Clients

- **CLI:** terminal UI by default. Arrow keys scroll, `c` clears the log, `q` / Ctrl+C disconnects. Use `-plain` for text output; redirected input or output selects it automatically.
- **GUI:** paste a token, connect, watch commands and live output. Follows the system theme; interface text is currently Chinese. [Build instructions below](#details).

Both clients keep recent logs in memory (~64 KiB), never save tokens, and require manual reconnection. Closing the client disconnects it.

Under the same OS account, CLI and GUI share an exclusive local connection lock for each server address, including different invitations. A duplicate attempt exits before redeeming its invitation; disconnecting or exiting the process releases the lock. Different server addresses can connect independently. Lock files live in `~/.agenterm/locks`, contain no credentials, and are separate from the download cache. Do not delete them while a client is running; their existence alone does not block a connection. This check requires updated clients and does not merge different DNS aliases for a server.

## Details

<details>
<summary>Invitations & downloads</summary>

Release servers provide CLI archives for all six OS/architecture combinations and their checksum manifest. One-command installers fetch the matching script from the server and first show a remote control warning and request authorization. After confirmation, they validate the invitation and version compatibility, probe the WebSocket handshake on `/agenterm/client`, then verify the cache or download the matching CLI and connect without another prompt. Cancelling stops immediately without probing or downloading the client. Probes do not redeem invitations or register clients. After authorization, installation shows four connection stages: connectivity check, client preparation, connecting, and connected. Terminal downloads display transfer progress; verified cached clients skip the download. No startup service is installed. The GUI is a separate download. Locally built servers do not provide download commands.

Commands use a short `/agenterm/c/<22-character invitation ID>` endpoint, with a `.ps1` suffix for Windows. The server dynamically fills in the invitation, version, and download URL. The ID reuses the existing 128-bit random invitation credential. Fetching the script does not consume the invitation, and responses disable caching. Treat the URL as an invitation: share it privately and exclude it from proxy access logs.

Invitations expire after 15 minutes and can be redeemed once. In the server console, Enter replaces unused startup invitations; existing connections and MCP-created invitations are unaffected. Redeemed client tokens remain reusable until the server restarts, with one connected client per token. Share credentials privately; invitation commands may remain in shell history.

The target only needs access to `-public-url`; installation and connection do not access GitHub. `127.0.0.1` works only locally. Reverse proxies only need to forward `/agenterm/`, preserving the prefix and allowing WebSocket upgrades and the Authorization header. Failed probes stop installation before downloading the manifest or CLI. After a successful probe, each invocation fetches the checksum manifest from the server and verifies the cached CLI, downloading it again if missing or damaged. Cache locations, partitioned by version, OS, and architecture:

| OS | Cache |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

The cache remains after disconnecting; delete it manually to remove it. Each connection still requires authorization. Scripts and assets use the server’s HTTP(S) origin without a pinned script hash; use HTTPS for public deployments. Checksums detect corruption; they do not replace code signing or protect against a compromised server or release. macOS/Linux use gzip archives and Windows uses zip. The server prepares the download archives in memory before accepting connections; extraction still happens on the target computer.

</details>

<details>
<summary>Authentication & deployment</summary>

The server defaults to `127.0.0.1:8080` and generates a new 10-character lowercase alphanumeric management key on each restart. Set a longer random key with `--passwd` for public deployments:

```sh
./agenterm-server -listen 127.0.0.1:8080 \
  -public-url https://agenterm.example.com \
  --passwd 'replace-with-a-long-random-key'
```

Configure an HTTPS reverse proxy with WebSocket support separately. `-public-url` accepts an origin or an address ending in `/agenterm`; advertised endpoints always use the `/agenterm/` prefix. It does not enable TLS. When the proxy is on the same machine, keep the backend bound to loopback. Public deployments also need appropriate access controls.

MCP accepts `Authorization: Bearer <management key>` or `Authorization: Bearer <client token>`. Management calls select a target with `client_token`; ordinary client tokens cannot call `create_client`. You can also configure one MCP entry per target. Your agent must support Streamable HTTP and custom headers.

`create_client` returns `client_token`, `invitation`, `expires_at`, and `display_text`. Release builds also return `macos_linux_command` and `windows_powershell_command`; both redeem the same invitation, so use one. All tokens and invitations expire on restart, even with a fixed management key.

`-no-password` disables MCP management authentication: anyone who can reach the endpoint can create and control connections. Keep this mode private. It cannot be combined with `--passwd`.

### Limit clients by issuing IP

`--max-clients-per-ip 5` limits each issuing IP to five connected clients; the default `0` disables this limit. The issuing IP is the MCP caller of `create_client`, not the invited machine. This works in either authentication mode.

While fewer than five clients are online, a new invitation can be created. Online clients plus tokens that have never connected are capped at five per issuing IP. If pending capacity is full, creating an invitation revokes the oldest never-connected token and its invitation, including an already redeemed invitation. Expired unredeemed invitations and their never-connected tokens are removed on the next creation. Creation is rejected when all slots are online; reconnects also check the limit. Previously connected tokens survive disconnects and new invitations; Server restart invalidates all tokens. Startup tokens from `--generate` have no issuing IP and are exempt.

When the IP quota is enabled, the Server reads the issuing IP from the `X-Real-IP` header. Creation requests with missing, multiple, or invalid values are rejected. Restrict direct access to the Server to a trusted reverse proxy, and have the proxy overwrite this header; otherwise direct callers can spoof their IP.

For Cloudflare → Nginx → Agenterm, enable Nginx's `ngx_http_realip_module`, configure `set_real_ip_from` for all Cloudflare IPv4 and IPv6 ranges and keep them current, set `real_ip_header CF-Connecting-IP;`, and set `proxy_set_header X-Real-IP $remote_addr;` in the Agenterm proxy location. Trust only Cloudflare ranges, never all sources. See [Cloudflare's visitor IP guidance](https://developers.cloudflare.com/support/troubleshooting/restoring-visitor-ips/restoring-original-visitor-ips/) and [IP ranges](https://www.cloudflare.com/ips/). Pseudo IPv4 header overwriting replaces the original IPv6 address and should be disabled for accurate quotas. IP limits cannot prevent IP rotation; users behind the same NAT share a quota.

Versions follow [SemVer 2.0.0](https://semver.org/). Connections validate versions in both directions using this policy:

- Stable versions at or above `1.0.0`: matching major numbers are compatible; minor and patch numbers may differ. New features within a major version must preserve existing wire behavior.
- `0.x`: SemVer does not guarantee API stability during development. Agenterm requires matching major and minor numbers; `0.0.x` also requires matching patch numbers.
- Prereleases (`alpha/beta/rc`, etc.): the version core and prerelease identifiers must match exactly. They cannot connect to stable releases.
- Build metadata (`+build`, etc.) does not affect compatibility. Missing or malformed version information is rejected.

Checks cover installation probes, invitation redemption, and CLI/GUI WebSocket connections. Rejection does not consume the invitation or register a client.

Client tokens contain the server address and a 128-bit random credential. All endpoints use the `/agenterm/` prefix. Previous root routes and token formats are unsupported. Update Server and Client (including the GUI) together and change the MCP URL to `/agenterm/mcp`.

</details>

<details>
<summary>Session behavior & platform limits</summary>

- After `create_client` succeeds, the agent must first show `display_text`, then call `wait_for_client` with the returned `client_token`. Only execute commands or start sessions after it returns `{"status":"connected"}`. `wait_for_client` may also return `{"status":"timeout"}`. `timeout_ms`: default 90000 (90 seconds), range 0–300000; 0 checks immediately. The agent host must allow the chosen duration; call again after a timeout if needed. This sequence is instructed through tool descriptions, not enforced by the server.
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

Release CI embeds the six uncompressed CLI binaries together in a single XZ-compressed tar archive. At startup, a pure Go decoder reconstructs standard gzip/ZIP downloads and SHA256SUMS in memory before the server prints invitations or starts listening. This reduces the server executable at the cost of startup CPU and memory, without requiring extra tools on the server or target computers. The release packer and server share deterministic archive generation and use the same pinned Go toolchain; CI compares every reconstructed download and its manifest with the release files. Do not recompress the published CLI archives independently. Artifact sizes appear in the workflow summary. GUI icons are losslessly recompressed without resizing or changing their pixels or metadata.

</details>

<details>
<summary>Probe MCP with curl</summary>

```sh
curl -s http://127.0.0.1:8080/agenterm/mcp \
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

## Language

The desktop client follows your system language by default. Use **Language / 语言** in the application menu to choose English, Simplified Chinese, or the system default; restart the application to apply the change. The terminal client and server follow the terminal locale. Unsupported languages fall back to English. Set `AGENTERM_LANG=en` or `AGENTERM_LANG=zh-CN` to override the language for a terminal process and its installer. Command output remains unchanged.

Translation resources are maintained in `internal/i18n/messages.json` with stable keys and both `en` and `zh-CN` values. Go code references keys; installer scripts receive their translations from the same resource at runtime. Add new user-facing messages there, keeping format placeholders consistent across languages.
