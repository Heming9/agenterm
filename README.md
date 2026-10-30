# Agenterm

<img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm app icon" width="128">

English | [简体中文](README.zh-CN.md)

Agenterm lets an agent execute commands on another computer through MCP. A server exposes the MCP endpoint, and a client connects to it over WebSocket. The current version supports one connected client at a time.

```text
Agent ── HTTP / MCP ──> Server <── WebSocket ── Client ──> shell
```

## Security

**The MCP endpoint has no authentication. Anyone who can reach the server can execute arbitrary commands on the connected client with the client's user permissions. Use it only on a trusted network or in a controlled test environment; do not expose it directly to the public internet.** The token authenticates the client only; it does not protect the MCP endpoint. Tokens can be reused until the server restarts. Clients do not save tokens or reconnect automatically.

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

The graphical client uses the same client core and follows the system light or dark theme. Enter the token to connect; the window shows connection status, commands, live output, exit codes, and durations. It also shows the number and average duration of completed commands, including failed or cancelled commands. Clearing output only clears the display; a successful reconnection resets the statistics. Connection attempts can be cancelled, and closing the window disconnects the client.

Logs are held in memory and retain approximately the latest 64 KiB. Commands and output may contain sensitive information. The GUI does not save tokens or reconnect automatically. The current interface text is in Chinese.

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
```

The GUI uses Fyne and requires CGo and a C compiler. Linux also requires graphics development libraries. Cross-compiling it requires a C cross-compiler for the target platform. On Windows, build locally with `go build -ldflags=-H=windowsgui -o Agenterm.exe ./cmd/agenterm-gui` to avoid an extra console window. See the [Fyne setup documentation](https://docs.fyne.io/started/) for platform dependencies.

To package the GUI with its system application icon, install the Fyne CLI matching the project's Fyne version, then run the package command on the target platform:

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
cd cmd/agenterm-gui
fyne package -os darwin   # macOS: Agenterm.app
fyne package -os windows  # Windows: Agenterm.exe
```

Run the command for your operating system; Windows packaging requires a working Windows C compiler. The Fyne CLI increments the build number in `FyneApp.toml` after packaging.

## Run

Start the server on a machine reachable by both the client and agent:

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

Replace `SERVER_IP` with the reachable address. The server prints an `agenterm:` connection token and the MCP endpoint. The default listen address and public URL use `127.0.0.1:8080`, suitable for local testing. Treat the token as a password: it contains the server address and a 128-bit random credential. It expires when the server restarts. Server and client must use the same token format; the current format is incompatible with the older format.

Run the client on the target computer:

```sh
./agenterm-client
```

Paste the token at the prompt, or enter it in the desktop client. The client runs commands as the current user; it does not require an SSH service or a separate system login.

Add `http://SERVER_IP:8080/mcp` as a remote MCP server in an agent that supports Streamable HTTP. For a non-interactive command, call `execute_command` with a `command` argument, for example:

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
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_command","arguments":{"command":"echo hello"}}}'
```

The server itself serves HTTP. For encrypted transport between devices, use an HTTPS reverse proxy that supports WebSocket and set `-public-url` to its HTTPS origin. HTTPS alone does not authenticate MCP requests and does not make public exposure safe.

## Limitations

- Interactive sessions use a PTY on macOS and Linux. Windows uses ordinary input/output pipes: simple prompts work, but programs requiring a real terminal do not. Remote graphical interfaces are not supported.
- Full-screen terminal programs emit control characters that agents may not understand. Terminal input can be echoed; avoid entering passwords or other secrets through an agent.
- Only one client can connect at a time. Reconnect after a server restart or connection loss.
- `execute_command` returns output at completion; use `session_start` and `session_read` to read while a command runs.
- Windows output is decoded as UTF-8 or the system code page based on the first non-ASCII content. Mixed encodings can still produce garbled output.
- Commands run with `/bin/sh -c` on macOS/Linux and `cmd.exe /C` on Windows.

## Test

```sh
go test ./...
```
