# Agenterm

<img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm app icon" width="128">

English | [简体中文](README.zh-CN.md)

Agenterm lets an agent execute commands on another computer through MCP. A server exposes the MCP endpoint, and a client connects to it over WebSocket. Multiple clients can connect simultaneously, each with its own token.

```text
Agent ── HTTP / MCP ──> Server <── WebSocket ── Client ──> shell
```

## Security

**The server uses unencrypted HTTP and WebSocket by default. Anyone able to observe traffic can read tokens, commands and output, and anyone with a token can execute commands on its connected client with that user’s permissions. Use only on a trusted network or in a controlled test environment; do not expose it directly to the public internet.** Each token authenticates and selects one client for MCP requests. Tokens can be reused until the server restarts. Clients do not save tokens or reconnect automatically.

Authentication failures are limited by directly connected source IP: 10 failures within one minute block that IP for five minutes, including valid credentials during the block. Users behind a shared IP affect each other. The server does not trust forwarded headers; behind a reverse proxy, requests may share the proxy IP, so also rate-limit by real source IP at the proxy. Distributed sources can still attempt authentication.

The server can see commands and their output. Commands also appear on the client, so secrets in commands may be visible. Closing the client disconnects it and attempts to stop running commands, but completed operations cannot be undone and detached child processes may continue running.

## Build

Install Go 1.24.3 or later, then clone the repository:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
```

Build the command-line programs:

```sh
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

The command-line client can be cross-compiled for the target computer, for example:

```sh
GOOS=linux GOARCH=amd64 go build -o agenterm-client-linux-amd64 ./cmd/agenterm-client
GOOS=windows GOARCH=amd64 go build -o agenterm-client-windows-amd64.exe ./cmd/agenterm-client
```

## Desktop client

The graphical client uses the same client core and follows the system light or dark theme. Enter the token to connect; the window shows connection status, commands, and live output. It also shows the number and average duration of completed commands, including failed or cancelled commands. Clearing output only clears the display; a successful reconnection resets the statistics. Connection attempts can be cancelled, and closing the window disconnects the client.

Logs are held in memory and retain approximately the latest 64 KiB. Commands and output may contain sensitive information. The GUI does not save tokens or reconnect automatically. The current interface text is in Chinese.

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
```

The GUI uses Fyne and requires CGo and a C compiler. Linux also requires graphics development libraries. Cross-compiling it requires a C cross-compiler for the target platform. On Windows, build locally with `go build -ldflags=-H=windowsgui -o Agenterm.exe ./cmd/agenterm-gui` to avoid an extra console window. See the [Fyne setup documentation](https://docs.fyne.io/started/) for platform dependencies.

To package the GUI with its system application icon, install the Fyne CLI matching the project's Fyne version, then run the package command on the target platform:

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
cd cmd/agenterm-gui
fyne package -os darwin --use-raw-icon   # macOS: Agenterm.app
fyne package -os windows -icon assets/app-icon-windows.png  # Windows: Agenterm.exe
```

Run the command for your operating system; Windows packaging requires a working Windows C compiler. The Fyne CLI increments the build number in `FyneApp.toml` after packaging.

## Run

Start the server on a machine reachable by both the client and agent:

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

Replace `SERVER_IP` with the reachable address. The server prints five `agenterm:` connection tokens and the MCP endpoint by default; use `-clients N` to issue a different number (1–1000). Each token selects one client and can have only one client connected at a time. The default listen address and public URL use `127.0.0.1:8080`, suitable for local testing. Treat the token as a password: it contains the server address and a 128-bit random credential. It expires when the server restarts. Server and client must use the same token format; the current format is incompatible with the older format.

Run the client on the target computer:

```sh
./agenterm-client
```

Paste the token at the prompt, or enter it in the desktop client. The client runs commands as the current user; it does not require an SSH service or a separate system login.

### Connect with a one-time invitation

A release build of the server prints installer commands for macOS/Linux and Windows PowerShell for each connection token. Send the appropriate command privately to the other user. It downloads the installer and command-line client for the server's release from GitHub, detects the architecture, and asks for authorization in the terminal before connecting. Closing the client disconnects it; no startup service is installed. The GUI remains available separately from the release and can connect using a token.

Each invitation expires after 15 minutes and can be redeemed once. Press Enter in the server console to replace all unused invitations with fresh ones; existing connections are unaffected. The connection token retains its existing lifetime and each token supports one connected client at a time. Treat invitation commands and tokens as credentials; commands may remain in shell history.

The other computer must be able to reach GitHub and the server's `-public-url`; the default `127.0.0.1` only works locally. Use an HTTPS reverse proxy with WebSocket support to protect invitations and credentials in transit. The installer script is fetched over HTTPS from GitHub without a separate pinned hash. It checks client downloads against the release checksum manifest, which detects download corruption but does not replace code signing or protect against a compromised release. Locally built servers do not print download commands; use a release build.

After sharing the corresponding invitation or token, the agent can call `wait_for_client` to wait for that target to connect and then continue. It returns `{"status":"connected"}` or `{"status":"timeout"}`. `timeout_ms` defaults to 30000 and accepts 0–300000; 0 checks immediately. The agent may call it again after a timeout. The agent host must display the invitation before waiting and allow the tool call to run for the chosen duration; the server does not provide invitations through MCP.

Add `http://SERVER_IP:8080/mcp` as a remote MCP server in an agent that supports Streamable HTTP and custom headers. Set the header `Authorization: Bearer <token>` to the same full `agenterm:` token used by the target client. Configure a separate MCP entry for each target if needed; support for custom headers varies by agent. For a non-interactive command, call `execute_command` with a `command` argument, for example:

```json
{"command":"pwd && ls"}
```

The call returns output and the exit code when the command completes. For interactive commands or incremental output:

1. Call `session_start` with `command` and retain the returned `session_id`.
2. Call `session_read` with `session_id`. Pass the returned `next_cursor` as the next `cursor`; set `wait_ms` to wait for output (up to 20000 ms). When `done` is true, the response includes `exit_code`.
3. Call `session_write` with `session_id` and `input` to answer prompts. Input typically needs a newline.
4. Call `session_stop` to stop a session.

Output is retrieved by repeated `session_read` calls rather than pushed automatically. The client also displays commands and output locally. Each session retains the latest 1 MiB of output; a reader that falls behind receives `truncated: true`. Finished sessions are retained for at least 30 minutes and cleaned up when a new session is created. MCP configuration and remote-server support vary by agent.

You can also inspect the MCP endpoint directly:

```sh
curl -s http://127.0.0.1:8080/mcp \
  -H 'Authorization: Bearer YOUR_AGENTERM_TOKEN' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_command","arguments":{"command":"echo hello"}}}'
```

The server itself serves HTTP and warns when `-public-url` uses HTTP. For encrypted transport between devices, use an HTTPS reverse proxy that supports WebSocket and set `-public-url` to its HTTPS origin. When the proxy and server run on the same machine, bind the backend to `127.0.0.1:8080`. Setting an HTTPS public URL does not enable TLS by itself; the reverse proxy must be configured separately. HTTPS protects traffic in transit, but tokens must still be kept secret; public exposure requires appropriate access controls.

## Limitations

- Interactive sessions use a PTY on macOS and Linux. Windows uses ordinary input/output pipes: simple prompts work, but programs requiring a real terminal do not. Remote graphical interfaces are not supported.
- Full-screen terminal programs emit control characters that agents may not understand. Terminal input can be echoed; avoid entering passwords or other secrets through an agent.
- Each token permits one connected client. Tokens expire on server restart; reconnect with a newly issued token after a restart or connection loss.
- `execute_command` returns output at completion; use `session_start` and `session_read` to read while a command runs.
- Windows output is decoded as UTF-8 or the system code page based on the first non-ASCII content. Mixed encodings can still produce garbled output.
- Commands run with `/bin/sh -c` on macOS/Linux and `cmd.exe /C` on Windows.

## Test

```sh
go test ./...
```

## License

Licensed under the [Apache License 2.0](LICENSE).
