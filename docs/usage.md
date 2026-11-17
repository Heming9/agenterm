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

For self-hosting, use the MCP address shown by the server (default path `/agenterm/mcp`). Configure the management key as the Streamable HTTP bearer credential so the client sends `Authorization: Bearer <management key>`. For Codex, set `AGENTERM_MANAGEMENT_KEY` in the environment that runs Codex, then register the address:

```sh
codex mcp add agenterm --url https://YOUR_HOST/agenterm/mcp --bearer-token-env-var AGENTERM_MANAGEMENT_KEY
```

Make sure the agent process can read the variable after restarting. Keep the key private.

## 🤝 Start a remote assistance session

1. Describe the task to your agent and ask it to create an invitation for the target computer.
2. Privately share the short link or the connection command for the target OS. The recipient opens the link or runs the command on the target computer, reads the notice, and authorizes access.
3. Keep the agent conversation active. Start the task once the device connects, then disconnect when finished.

Short links are available for single-device invitations when short codes are enabled. Self-hosted servers disable short codes by default. To connect several computers, ask for a multi-device invitation and identify the target before each operation.

## 💻 Client controls

The current source builds the [Rust CLI](../tools/agenterm-cli/README.md). The client registers the `agenterm-cli:` protocol when launched normally. After registration, select **Open terminal client and connect** on the invitation page and choose temporary or persistent access. For first-time setup or if the browser cannot launch the client, run the command shown on the page or paste the connection token or full short-code address into the client.

A bare short code requires saved server history. Enter the code and select the server that issued it. If you have no server history, paste the full address. Keep the `https://` prefix for HTTPS addresses.

Temporary connections disconnect after five minutes of inactivity, excluding time spent running commands. Persistent connections have no such idle timeout. Set `-connection-mode=temporary` or `-connection-mode=persistent` when launching the CLI directly. The default is temporary. The invitation page and install command use the mode selected during authorization.

Use the arrow keys or mouse wheel to browse output and drag to select and copy text. With no active selection, Ctrl+C disconnects and returns to the connection form. There, Ctrl+C clears a populated token field or exits if it is empty.

The one-command connection stores a client in a local cache. You can delete it after closing the client:

| OS | Cache directory |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

## ❓ Troubleshooting

### Connection failures or expired invitations

Make sure both sides can reach the service. An address such as `127.0.0.1` works only on the server itself. If versions are incompatible, use the client from the same release as the server. If a single-device invitation reports an active connection, close the other client first.

The first connection has a default 15-minute deadline. An active connection stays valid. After the last device disconnects, the device token and any still-usable short code have a three-minute reconnect window. A short code expires after its tenth successful redemption. Opening the page does not count. Ask the agent for a new invitation if the credential no longer works.

### Network interruptions

The client attempts to reconnect. The same client process can resume access to retained terminals and output. Ask the agent to check the device and task state before continuing. Commands are never replayed automatically. Keep the agent conversation active so assistance can continue when the device comes back online.

### Can I control a remote desktop?

Agenterm provides terminal assistance, not a remote desktop. Windows supports simple interactive input; full-screen programs requiring a real terminal may not work.

### How do I transfer files?

Use `p2p_transfer_start` through MCP to start file transfer on a device connected with the Rust CLI. The devices must be able to connect directly, and each transfer handles one file. Signaling requires HTTPS for cross-device use. HTTP is allowed on loopback addresses. Closing the client stops its P2P helper. See the [P2P component guide](../tools/p2p-cli/README.md).

### Permissions and privacy

A connection grants terminal access with the target computer's current user permissions; authorize only people you trust. The agent, server, and target client can see commands and output. Keep invitations and tokens private, and avoid sending passwords or other secrets through sessions. Disconnecting cannot undo completed operations. Use HTTPS on untrusted networks.

Developers can consult the [MCP tool reference](mcp-tools.md).
