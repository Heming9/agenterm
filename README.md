<div align="center">
  <img src="cmd/agenterm-client-gui/assets/app-icon-agent.png" alt="Agenterm" width="96">
  <h1>Agenterm</h1>
  <p><strong>Let an AI agent assist with a remote computer through one authorized connection command.</strong></p>
  <p>💻 macOS/Linux/Windows · 🖥️ CLI/GUI · 🔗 Multiple devices · ⚡ One-click connection · 🪶 Lightweight · 🔓 No SSH required · 🤖 Agent-first</p>
  <p>
    <a href="#-quick-start">🚀 Quick start</a> ·
    <a href="https://github.com/Heming9/agenterm/releases">📥 Download client</a> ·
    <a href="docs/deployment.md">🏠 Self-host</a> ·
    <a href="README.zh-CN.md">简体中文</a>
  </p>
</div>

<p align="center">
  <a href="docs/demo/agenterm-cleanup.mp4"><img src="docs/demo/quickstart.gif" alt="An agent creates an invitation, the target computer authorizes access, and the task runs" width="800"></a>
</p>
<p align="center">Create invitation → Authorize access → Connect device → Run task</p>
<p align="center"><a href="docs/demo/agenterm-cleanup.mp4">Full demo · 1 min 42 sec</a></p>

## 🚀 Quick start

<p align="center">
  <img src="docs/design/connection-flow-en.gif" alt="Connection workflow: connect Agent to MCP, ask Agent to generate a connection command, share the command, the recipient runs it and grants access, then Agent provides remote assistance" width="1000">
</p>

1. **Connect your agent:** Add the MCP service `https://agenterm.net/mcp` to an agent that supports Streamable HTTP. [See configuration examples](docs/usage.md#connect-your-agent).
2. **Invite and authorize:** Ask the agent to create an invitation and privately share the connection command. The other person runs it and authorizes access.
3. **Start assisting:** Once the device connects, tell the agent what to do. The other person can see commands and output and disconnect at any time.

Connections grant terminal access with the target computer's current user permissions. Only authorize people you trust.

## 💡 When to use it

- **Help a colleague or customer troubleshoot:** Let your agent inspect the remote environment and logs without passing commands and output back and forth.
- **Help family with computer problems:** Once they run the client and authorize access, use your familiar agent to assist through the terminal.
- **Work on another development machine or server:** Keep using your existing agent without configuring a full agent and model account on the target machine.
- **Check multiple computers:** Connect test machines, inspect their environments, and summarize the results.

Interactive terminal tasks are supported. Use the CLI or GUI on the target computer to follow progress; the client automatically attempts to reconnect after temporary network failures.

## 🔗 How it works

<p align="center">
  <img src="docs/design/architecture-blue.gif" alt="Agenterm architecture: AI Agent connects over MCP to the Server, which relays commands and results over WebSocket to the Client and local shell" width="1000">
</p>

The agent and target computer connect through the server. After running the client, the person at the target computer can authorize terminal assistance without opening an inbound port.

Use the hosted service or [deploy your own server](docs/deployment.md).

## ❓ FAQ

<details>
<summary>What if a connection fails or an invitation expires?</summary>

Check that both sides can reach the service. If an invitation no longer works, ask the agent to create a new one. For version mismatches, use the server and client from the same release. [Connection troubleshooting](docs/usage.md#connection-failures-or-expired-invitations).

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

Management access currently requires no authentication. The number of online devices is limited per invitation creator's IP address. The agent, server, and target client can see commands and output, so keep invitations and tokens private. Use HTTPS when self-hosting publicly.

</details>

[User guide](docs/usage.md) · [Deployment and source builds](docs/deployment.md) · [Report an issue](https://github.com/Heming9/agenterm/issues) · [Apache-2.0](LICENSE)
