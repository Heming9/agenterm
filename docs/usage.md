# User guide

[简体中文](usage.zh-CN.md) · [Home](../README.md) · [Self-hosting](deployment.md)

## Connect your agent

### 🤖 Ask your agent to configure it

Send this to your agent:

```text
Please configure Agenterm as an MCP server for this agent using Streamable HTTP at https://agenterm.net/mcp. Preserve my existing configuration, check the connection afterward, and tell me if I need to restart or reload.
```

### ⚙️ Configure it manually

These examples use the hosted Agenterm service. If a configuration file already exists, merge the `agenterm` entry rather than replacing other settings.

**Codex:** Run in a terminal (applies to the CLI and IDE extension):

```sh
codex mcp add agenterm --url https://agenterm.net/mcp
```

**Claude Code:** Run in a terminal to add it for all projects for your user:

```sh
claude mcp add --transport http --scope user agenterm https://agenterm.net/mcp
```

**Gemini CLI:** Run in a terminal to add it to your user configuration:

```sh
gemini mcp add --transport http --scope user agenterm https://agenterm.net/mcp
```

Reload the MCP configuration or restart your agent, then check its MCP settings for the connection status. Other agents that support Streamable HTTP can use the same URL.

For self-hosting, use the MCP address printed at startup and configure your agent to send the management key as an HTTP Bearer credential. Keep the key out of version control.

## 🤝 Start a remote assistance session

1. Describe the task to your agent and ask it to create an invitation for the target computer.
2. Privately send the invitation link to the person at the target computer. When the server issues an invitation without a short link, share the connection command for their operating system. Keep the management key and `control_token` with the agent.
3. On the target computer, open the invitation link or run the shared connection command. If you have the updated Rust CLI, choose **I have**, then **Open terminal app**. For first-time setup or if the browser cannot open the updated client, run the command for your operating system. Review the server and approve or deny remote control in the CLI, not on the web page.
4. Keep the agent conversation active while the device connects, then start the task. The other person can see commands and output in the client and disconnect at any time.
5. Close or disconnect the client when finished.

### Invitation lifetime and short codes

Short links and codes are available for single-device invitations when the server administrator enables short codes. To connect multiple computers, ask the agent for a multi-device invitation, share the connection command, and identify the target device before each operation.

While a single device is connected, its short link, code, and connection token cannot start another connection. They become available again after disconnection if the token is still valid. The tenth successful short-code redemption retires the link and code. Opening the invitation page does not count toward that limit.

After a successful connection, the client saves the server address locally. Entering a six-character code opens a list of saved servers; choose the one that issued it. The client contacts only the selected server and preserves its HTTP or HTTPS scheme. With no history, paste the short-code address as issued. The CLI uses HTTP for addresses without a scheme. For HTTPS, keep the `https://` prefix.

## 💻 Client controls

The maintained client is the [Rust CLI](../components/agenterm-cli/README.md). Paste a connection token or short-code address to connect. Use the arrow keys or mouse wheel to scroll and drag to select and copy text. Press Esc to clear a selection, then Ctrl+C to disconnect and return to the connection form. On that form, Ctrl+C clears the token first, then quits.

Temporary connections disconnect after five minutes of inactivity, excluding time spent running commands. Persistent connections stay open until disconnected. Regardless of how you open the invitation, choose the mode in the CLI's authorization prompt. See [how the Rust CLI registers browser links](../components/agenterm-cli/README.md#protocol-links).

The one-command connection stores a client in a local cache. You can delete it after closing the client:

| OS | Cache directory |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

### Temporary assistance invitations

Run the command supplied with the invitation and approve the connection. The installer downloads and runs the client in the system temporary directory. Agenterm streams terminal output without creating a local session log. It can write temporary scripts to run commands. On normal exit the installer removes the downloaded client and its temporary files. Changes made by the assistance task are preserved. If the installer and client are both forcibly stopped, or the device loses power, temporary files can remain.

## ❓ Troubleshooting

### Connection failures or expired invitations

Make sure both the agent and target computer can reach the service; `127.0.0.1` only works on the same computer. If an invitation no longer works, ask the agent to create a new one. For version mismatches, use the server and client from the same release. If another connection is active, close the other client first.

### Network interruptions

The client automatically attempts to reconnect without replaying commands. With resume credentials, a single-device CLI process can replace its own stale connection after an interruption. Keep the client open and ask the agent to check the device and task state after reconnecting. Connecting a device does not wake an agent conversation that has already ended.

### Can I control a remote desktop?

Agenterm provides terminal assistance, not a remote desktop. Windows supports simple interactive input; full-screen programs requiring a real terminal may not work.

### How do I transfer files?

Use `p2p_transfer_start` through MCP to start a direct connection to a device connected with the Rust CLI. Once both ends are connected, you can copy files in either direction over the same connection, one file per copy. Closing the client stops its P2P helper. See the [P2P component guide](../components/p2p-cli/README.md).

### Permissions and privacy

A connection grants terminal access with the target computer's current user permissions; authorize only people you trust. The agent, server, and target client can see commands and output. Keep invitations and tokens private, and avoid sending passwords or other secrets through sessions. Disconnecting cannot undo completed operations. Use HTTPS on untrusted networks.

Developers can consult the [MCP tool reference](mcp-tools.md).
