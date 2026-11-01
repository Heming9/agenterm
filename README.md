<div align="center">
  <img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm" width="96">
  <h1>Agenterm</h1>
  <p><strong>Let an AI agent assist with a remote computer through one authorized connection command.</strong></p>
  <p>macOS / Linux / Windows · CLI / GUI · Multiple devices · No SSH service required</p>
  <p>
    <a href="#quick-start">Quick start</a> ·
    <a href="https://github.com/Heming9/agenterm/releases">Download client</a> ·
    <a href="docs/deployment.md">Self-host</a> ·
    <a href="README.zh-CN.md">简体中文</a>
  </p>
</div>

<p align="center">
  <a href="docs/demo/agenterm-cleanup.mp4"><img src="docs/demo/quickstart.gif" alt="An agent creates an invitation, the target computer authorizes access, and the task runs" width="800"></a>
</p>
<p align="center">Create invitation → Authorize access → Connect device → Run task</p>
<p align="center"><a href="docs/demo/agenterm-cleanup.mp4">Full demo · 1 min 42 sec</a> · <a href="docs/demo/subtitles.zh-en.srt">Chinese / English subtitles</a></p>

## Quick start

### 1. Person requesting help: connect your agent

Add this MCP service to an agent that supports Streamable HTTP:

```text
https://agenterm.net/mcp
```

[See configuration examples](docs/usage.md#connect-your-agent). Describe your task, ask the agent to create an invitation, and privately share the generated connection command with the person at the target computer.

### 2. Person at the target computer: connect and authorize

Run the invitation command for your operating system, read the notice, and authorize access. The client displays connection status, commands, and output. You do not need to configure MCP or a server.

### 3. Both: complete the task and disconnect

Keep the agent conversation active while the device connects, then start the task. The target computer can disconnect at any time; close the client when finished.

Invitations are single-use by default and expire after 15 minutes. Connections grant terminal access with the current user's permissions. Share invitations and tokens only with people you trust.

## What you can do

- **Troubleshoot development environments:** “Help my colleague check their Python setup and find why the project won't start.”
- **Inspect system status:** “Check this server's disk usage and application logs.”
- **Work across computers:** “Connect several test machines, check their environments, and summarize the results.”

Interactive terminal tasks are supported. Use the CLI or GUI on the target computer to follow progress; the client automatically attempts to reconnect after temporary network failures.

## How it works

```mermaid
flowchart LR
    A["AI Agent"] <-->|MCP| S["Server"]
    S <-->|WebSocket| C["Client"]
    C <--> T["shell"]
```

The server forwards commands and results. The client runs on the target computer, initiates its server connection, and executes tasks through the local shell. Both the agent and target need access to the server; the target needs no inbound port.

Use the hosted service or [deploy your own server](docs/deployment.md).

## FAQ

<details>
<summary>What if a connection fails or an invitation expires?</summary>

Check that both sides can reach the server. Ask the agent for a new invitation if one has expired, been used, or the server has restarted. For version mismatches, use server and client binaries from the same release. [Connection troubleshooting](docs/usage.md#connection-failures-or-expired-invitations).

</details>

<details>
<summary>Will tasks resume automatically after a disconnection?</summary>

The client attempts to reconnect but does not replay commands. Ask the agent to check the device and task state. Connecting a device does not wake an agent conversation that has already ended.

</details>

<details>
<summary>Can I control a desktop or connect several computers?</summary>

Agenterm provides terminal assistance, not a remote desktop. Ask the agent for a multi-device invitation to work across computers. Windows supports simple interactive prompts; full-screen programs requiring a real terminal may not work.

</details>

<details>
<summary>What are the hosted service limits?</summary>

Management access currently requires no authentication. Up to four devices can be online per invitation creator's public IP; people on the same network share that allowance. The agent, server, and target client can see commands and output, so keep invitations and tokens private. Use HTTPS when self-hosting publicly.

</details>

[User guide](docs/usage.md) · [Deployment and source builds](docs/deployment.md) · [Report an issue](https://github.com/Heming9/agenterm/issues) · [Apache-2.0](LICENSE)
