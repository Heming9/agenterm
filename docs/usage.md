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
