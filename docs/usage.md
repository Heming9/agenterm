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

Invitations expire after 15 minutes and are single-use by default. For several computers, ask the agent to create a multi-device invitation and clearly identify the target before each operation.

The hosted service currently requires no management authentication and allows up to four online devices per invitation creator's public IP. People on the same network share that allowance. Keep invitations and connection tokens private. Self-host to control management access yourself.

## MCP tool contracts

Current source builds publish explicit output schemas and behavior annotations for all seven tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `invitation`, `expires_at`, platform commands, `display_text`, `next_action`. |
| `list_clients` | Inspect devices or wait for a connection. | `clients`; polling also returns `status` and `next_action`. |
| `execute_command` | Run a short non-interactive command and wait for its result. | `stdout`, `stderr`, `exit_code`, `timed_out`, `canceled`, `truncated`, optional `error`. |
| `session_start` | Start an interactive or long-running process. | `session_id`, initial `cursor=0`. |
| `session_read` | Read incremental output and check completion. | `output`, `next_cursor`, `truncated`, `done`; terminal results add exit, timeout, cancellation and error fields. |
| `session_write` | Send input, including a newline to submit a prompt response. | `accepted`: input was sent, not confirmation it was consumed. |
| `session_stop` | Request cancellation and wait briefly for confirmation. | `accepted`, `done`, `status`, `already_done`, terminal fields when available, or `next_action`. |

Each `execute_command` starts a fresh shell. Set `cwd` and `env` explicitly; they do not persist across calls. Its timeout defaults to 60 seconds, with `timeout_ms=0` disabling it. Captured stdout/stderr share a 1 MiB limit. Use session tools for prompts, incremental output or long jobs, and retain the `session_id` and the byte-based `next_cursor`. Session buffers retain the latest 1 MiB; inspect `truncated` to detect missing older output.

`session_stop` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

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

Use `./agenterm-client -plain` for plain text; redirecting input or output selects it automatically. Copying over SSH requires a terminal that supports OSC 52 clipboard access.

**Desktop client (GUI):** paste a token, connect or disconnect, and watch live commands and output. It follows the system theme and shows connection latency and command statistics.

Clients keep up to 10 MiB of recent logs in memory and do not save connection tokens. Clearing logs affects only the local display.

## Troubleshooting

### Connection failures or expired invitations

- Check that both the agent and target computer can reach the server. `127.0.0.1` only works on the same computer.
- Ask the agent for a new invitation if one has expired or already been used.
- A server restart invalidates invitations and tokens; create a new connection afterward.
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
