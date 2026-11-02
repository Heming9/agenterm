# User guide

[简体中文](usage.zh-CN.md) · [Home](../README.md) · [Self-hosting](deployment.md)

## Connect your agent

Connect an agent that supports Streamable HTTP to:

```text
https://agenterm.net/mcp
```

For example, add this to `~/.codex/config.toml` for Codex:

```toml
[mcp_servers.agenterm]
url = "https://agenterm.net/mcp"
```

Reload the MCP configuration or restart your agent, then:

1. Describe your task and ask the agent to create a connection invitation for the target computer.
2. Privately share the generated connection command with the person at that computer.
3. They run the command for their operating system, read the remote-control notice, and authorize access.
4. Keep the agent conversation active while the device connects. Once connected, the agent can perform authorized work while the target computer displays progress.
5. Close or disconnect the client when finished.

Invitations expire after 5 minutes and are single-use by default. Set the optional `timeout_seconds` parameter to a positive integer when calling `create_client` to customize the lifetime; for example, `{"timeout_seconds": 60}` creates an invitation valid for 60 seconds. Expiry prevents invitation redemption and does not disconnect devices that have already connected. For several computers, ask the agent to create a multi-device invitation and clearly identify the target before each operation.

After a token has successfully connected, it remains valid while any of its devices is online. Once its last device disconnects, it is retained for 30 minutes for reconnection. Reconnecting within that period clears the offline expiry; otherwise the token expires and a new invitation is required. This retention period is separate from the invitation lifetime.

The hosted service currently requires no management authentication and allows up to four online devices per invitation creator's public IP. People on the same network share that allowance. Keep invitations and connection tokens private. Self-host to control management access yourself.

## MCP tool contracts

Current source builds publish explicit output schemas and behavior annotations for all seven tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_clients` | Inspect devices or wait for a connection. | `clients`; waiting calls also return `status`. |
| `execute_command` | Run a short non-interactive command and wait for its result. | `stdout`, `stderr`, `exit_code`, `timed_out`, `canceled`, `truncated`, optional `error`. |
| `session_start` | Start an interactive or long-running process. | `session_id`, initial `cursor=0`. |
| `session_read` | Read paginated output and check session completion. | `output`, `next_cursor`, `has_more`, `truncated`, `done`; terminal results add exit, timeout, cancellation and error fields. |
| `session_control` | Send input, interrupt, EOF or stop using `action`. | `action`; input returns `accepted`, interrupt/EOF return `delivered`, stop returns confirmation and terminal fields. |
| `session_list` | Rediscover this token's retained sessions, optionally filtered by `client_id`, even for offline devices. | `sessions`: session, device, original command, timestamps, lifecycle status and retained output cursor range. |

The MCP text content of `create_client` directly returns a fixed English plain-text connection template, identical to `structuredContent.display_text`, regardless of the server language setting. Credentials, expiry and platform commands remain available in `structuredContent` for programmatic use. The template contains the Windows command, macOS / Linux command, connection token, usage steps and invitation validity in that order. Multi-device mode appends an invitation reuse notice.

After generating connection instructions with `create_client`, the agent must present the complete `display_text` in the language used to communicate with the user. Translate only headings and explanatory prose; when communicating in English, present it verbatim. Preserve all sections, order, line breaks and plain-text format. Keep commands, URLs, connection tokens, validity numbers and expiry timestamps unchanged. Do not omit content or add Markdown formatting. Then call `list_clients` with the returned `client_token` and `wait_ms: 60000` to check for a successful connection, with a 60-second timeout. The call returns immediately when any device is online. If no device connects within 60 seconds, report the timeout to the user. These requirements are published in the MCP tool descriptions.

`session_control` requires `session_id` and `action`. Only `action=input` accepts and requires `input`; only `action=stop` accepts optional `wait_ms` (default 1000). Unrelated parameters are rejected. Every successful result includes its `action`, with separate result schemas for sent input, acknowledged control and stop confirmation. The tool is conservatively marked non-idempotent because input and interruption can repeat side effects; repeated stop remains safe as described below. Management access exposes seven tools; device-scoped access exposes six, excluding `create_client`.

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

`list_clients` returns `status`, `client_id`, `connected_at`, and a `client_info` object for each device. The device `status` displays `Connected`, with the latest server-client heartbeat RTT when available, such as `Connected · 203ms`. `client_info` contains `hostname`, `platform`, `arch` (client process architecture), `shell` (the interpreter path used for commands and sessions), `default_cwd` (the client working directory inherited when `cwd` is omitted), and `client_version` (the client application version reported during the handshake). Empty metadata fields mean unknown or not reported by the client. Both server and client must be updated to expose the new metadata.

Each `execute_command` starts a fresh shell. Set `cwd` and `env` explicitly; they do not persist across calls. Its timeout defaults to 60 seconds, with `timeout_ms=0` disabling it. Captured stdout/stderr share a 1 MiB limit. Use session tools for prompts, incremental output or long jobs, and retain the `session_id` and the byte-based `next_cursor`. Session buffers retain the latest 1 MiB; inspect `truncated` to detect missing older output.

To preserve working directory, variables, functions or an activated environment, explicitly launch a persistent shell with `session_start` (for example, `bash -i` where installed on Linux/macOS), then send commands through `session_control(action="input")` using the same `session_id`. Each `session_start` creates an independent process; a one-shot command leaves no persistent shell behind. Client disconnection ends sessions; reconnect does not restore shell state.

`session_read.max_bytes` defaults to 16384, with a range of 4–65536 bytes. Output never splits a UTF-8 character. Continue from each returned `next_cursor`. `has_more=true` means retained output remains; even with `done=true`, keep reading until `has_more=false`. `done` and `exit_code` describe the whole session process, not individual commands in a persistent shell. Pagination alone does not set `truncated`.

`session_list` returns this token's sessions in start-time order, including retained results for offline devices. `output_start_cursor` points to the earliest retained output; `output_end_cursor` points after the latest output, so reading from it skips existing output. Session `status` is `running`, `stopping` or `finished`; it does not infer shell idleness, current directory or individual command state. Finished records are retained for up to 30 minutes and may be evicted when new sessions start.

Before interrupt or EOF, check `list_clients.client_info.session_controls` (`interrupt` and/or `eof`). An empty list means unavailable; older clients may need updating. Both operations wait up to 5 seconds for client acknowledgment. `delivered=true` does not guarantee the program handled the operation; read output to verify. A timeout or cancellation leaves delivery uncertain, so do not blindly retry.

- `session_control(action="interrupt")` sends SIGINT to the current terminal foreground process group on Linux/macOS. An interactive shell may remain usable after its foreground command exits; the program may also ignore the signal or exit. This does not forcibly cancel the entire session like `session_control(action="stop")`. Windows pipe mode does not support interrupt.
- `session_control(action="eof")` follows previously queued input. On Linux/macOS canonical terminals it sends the configured VEOF character: pending input may be delivered first, requiring another EOF, and stdin is not permanently closed. Raw mode or disabled VEOF returns an error. On Windows pipes it permanently closes stdin while output remains readable; subsequent writes fail and repeated EOF is harmless. EOF at an idle shell prompt may exit the shell.

`session_control(action="stop")` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

| Status | Meaning and next step |
| --- | --- |
| `stopping` | Request sent or already pending, but termination is not confirmed. Continue `session_read` until `done=true`. |
| `stopped` | The target reported a terminal result with `canceled=true`. Read any remaining session output. |
| `finished` | The session ended for another reason. Inspect `exit_code`, `timed_out`, `canceled` and `error`; a disconnect does not confirm a remote cancellation. |

`accepted=true` alone never means the process has stopped. Repeating a stop while cancellation is pending does not send it again. A session already ended before the call returns `already_done=true` and `accepted=false` without an error; unknown or unauthorized sessions are errors. Stopping does not undo completed operations, revoke the device credential or stop other sessions. Do not blindly retry commands or input writes: they can repeat side effects.

## Client controls

**Terminal client (CLI):** opens a terminal interface by default. Multiline commands and output are folded; click to expand or collapse them.

| Action | Control |
| --- | --- |
| Browse logs | Arrow keys or mouse wheel. |
| Copy text | Drag to select; releasing the mouse copies it. Ctrl+C copies an existing selection again. |
| Select across screens | Hold the pointer at the top or bottom edge of the log. |
| Clear selection | Esc or click outside the log. |
| Clear logs | `c`. |
| Disconnect and exit | `q`, or Ctrl+C when no text is selected. |

Use `./agenterm-client-cli -plain` for plain text; redirecting input or output selects it automatically. Copying over SSH requires a terminal that supports OSC 52 clipboard access.

**Desktop client (GUI):** paste a token, connect or disconnect, and watch live commands and output. It follows the system theme and shows connection latency and command statistics.

Both clients accept `-token <token>` or `-invite <invitation>` (including the `-name=value` form), and `-help` lists the available flags. Use only one connection option at a time. An invitation requires authorization in the terminal or a GUI dialog. The CLI alone supports `-plain` and the installer's `-consent-confirmed` flag; the GUI rejects these flags and always requests authorization for invitations.

Clients keep up to 10 MiB of recent logs in memory and do not save connection tokens. Clearing logs affects only the local display.

## Troubleshooting

### Connection failures or expired invitations

- Check that both the agent and target computer can reach the server. `127.0.0.1` only works on the same computer.
- Ask the agent for a new invitation if one has expired or already been used.
- A Runtime restart invalidates invitations and tokens; create a new connection afterward. Restarting Server alone preserves connections and sessions.
- If versions are incompatible, update the server and clients from the same release.
- If a connection is already active, close the other CLI or GUI connected to that server under the same OS account.

### Network interruptions

The client automatically attempts to reconnect. It tries to cancel commands interrupted by disconnection and does not replay them afterward. Ask the agent to check the device and task state before continuing. Waiting requires the agent conversation to stay active; connecting a device does not wake a conversation that has already ended.

### Supported programs

Command-line tasks are supported on macOS, Linux, and Windows. Windows interaction supports simple input prompts; full-screen programs requiring a real terminal may not work. Each ordinary command runs independently. Ask the agent to use an interactive session for tasks requiring continuous input.

### Permissions and privacy

A connection token grants terminal access with the target computer's current user permissions. Obtain authorization before connecting and disconnect when finished. The agent, server, and target client can see commands and output, so avoid sending passwords or other secrets through sessions. Closing the client cannot undo completed operations.

Default HTTP connections are unencrypted. Use HTTPS across untrusted networks and keep management keys, invitations, and tokens private.

### Cache and removal

One-command connection downloads cache the CLI in:

| OS | Cache directory |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

Close the client before deleting the cache. One-command connection does not install a startup service; subsequent use still requires authorization.

### Language

The desktop client follows your system language. Choose English, Simplified Chinese, or the system default from **Language / 语言** in the application menu, then restart to apply it. The CLI and server follow the terminal locale; set `AGENTERM_LANG=en` or `AGENTERM_LANG=zh-CN` to override it. Command output remains unchanged.
