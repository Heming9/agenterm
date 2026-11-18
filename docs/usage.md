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

For self-hosting, replace the URL with the MCP address shown after deployment and configure the management key using your agent's settings. Do not commit the key to version control.

## 🤝 Start a remote assistance session

1. Describe the task to your agent and ask it to create an invitation for the target computer.
2. Privately share the connection command or short-code address with the person at that computer. Open the short link (for example, `example.com/c/abcdef`) in a browser on the target computer to view the commands and connection token. While a single-device client is online, its short link, code and connection token cannot be used to start another connection; they can be used again after disconnection while still valid. The short link and code follow the connection token’s lifetime. Viewing the invitation page does not count as a redemption; after the tenth successful client redemption the code and link become invalid. GET displays the invitation page; clients redeem using POST. Installer scripts use a separate connection-token secret in the path. The connection command follows the connection token’s lifetime. Share the link privately with trusted people because it provides access to credentials.

   When both the single-device client and server support resume credentials, the client keeps a private credential in its current process and can replace its own stale connection after a network interruption. Other devices cannot take over without it. Older clients or servers do not support this capability; update both for reliable automatic reconnection.
3. They run the command for their operating system, read the notice, and authorize access.
4. Keep the agent conversation active while the device connects, then start the task. The other person can see commands and output in the client and disconnect at any time.
5. Close or disconnect the client when finished.

To connect several computers, ask the agent for a multi-device invitation and identify the target before each operation. Multi-device tokens do not have short codes. Short codes for single-device connections are disabled by default and must be enabled by the server administrator.

After a successful connection, the client saves the server address locally. Entering a six-character code opens a list of saved servers; choose the one that issued it. The client contacts only the selected server and preserves its HTTP or HTTPS scheme. With no history, paste the short-code address as issued. The CLI uses HTTP for addresses without a scheme. For HTTPS, keep the `https://` prefix.

## 💻 Client controls

The maintained client is the [Rust CLI](../components/agenterm-cli/README.md). Paste a connection token or short-code address to connect. Use arrow keys or the mouse wheel to browse, drag to select and copy text, and press Ctrl+C to disconnect and return to the connection form. On that form, Ctrl+C clears the token first, then quits.

The invitation page retains **Open terminal client and connect** for protocol-link support. The Rust CLI does not handle those links yet; use the connection command or paste the token while this support is being added. Connection commands ask you to authorize temporary or persistent access, or deny it.

The terminal client accepts `-connection-mode=temporary` or `-connection-mode=persistent`; the default is `temporary`. One-click connections and invitations use the mode selected during authorization.

The one-command connection stores a client in a local cache. You can delete it after closing the client:

| OS | Cache directory |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

## ❓ Troubleshooting

### Connection failures or expired invitations

Make sure both the agent and target computer can reach the service; `127.0.0.1` only works on the same computer. If an invitation no longer works, ask the agent to create a new one. For version mismatches, use the server and client from the same release. If another connection is active, close the other client first.

### Network interruptions

The client automatically attempts to reconnect but does not replay commands. Ask the agent to check the device and task state before continuing. Connecting a device does not wake an agent conversation that has already ended.

### Can I control a remote desktop?

Agenterm provides terminal assistance, not a remote desktop. Windows supports simple interactive input; full-screen programs requiring a real terminal may not work.

### How do I transfer files?

Use `p2p_transfer_start` through MCP to start file transfer on a device connected with the Rust CLI. The two devices must be able to connect directly, and each transfer handles one file. Closing the client stops its P2P helper. See the [P2P component guide](../components/p2p-cli/README.md).

### Permissions and privacy

A connection grants terminal access with the target computer's current user permissions; authorize only people you trust. The agent, server, and target client can see commands and output. Keep invitations and tokens private, and avoid sending passwords or other secrets through sessions. Disconnecting cannot undo completed operations. Use HTTPS on untrusted networks.

Developers can consult the [MCP tool reference](mcp-tools.md).
