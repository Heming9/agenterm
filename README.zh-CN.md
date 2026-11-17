<div align="center">
  <img src="docs/design/app-icon-agent.png" alt="Agenterm" width="96">
  <h1>Agenterm</h1>
  <p><strong>让 AI Agent 通过授权的终端连接协助操作远程电脑。</strong></p>
  <p>
    <img src="https://img.shields.io/badge/平台-macOS%20%7C%20Linux%20%7C%20Windows-blue" alt="支持 macOS、Linux 和 Windows">
    <img src="https://img.shields.io/badge/界面-Rust%20CLI-7c3aed" alt="Rust CLI 客户端">
    <img src="https://img.shields.io/badge/MCP-Streamable%20HTTP-0891b2" alt="支持 MCP Streamable HTTP">
    <a href="LICENSE"><img src="https://img.shields.io/badge/许可证-Apache--2.0-green" alt="Apache 2.0 许可证"></a>
  </p>
  <p>🔗 多设备 · ⚡ 一键连接 · 🔓 无需 SSH · 🤖 Agent 优先</p>
  <p>
    <a href="#-快速开始">🚀 快速开始</a> ·
    <a href="https://github.com/Heming9/agenterm/releases">📥 发布版本</a> ·
    <a href="docs/deployment.zh-CN.md">🏠 自托管</a> ·
    <a href="README.md">English</a>
  </p>
</div>

<p align="center">
  <a href="docs/demo/README.md"><img src="docs/demo/quickstart.gif" alt="Agent 发出邀请，目标电脑授权连接，随后执行任务" width="800"></a>
</p>
<p align="center"><a href="docs/demo/README.md">演示视频和字幕 · 1:42</a></p>

## 🚀 快速开始

1. **接入 Agent。** 在支持 Streamable HTTP 的 Agent 中添加 MCP 地址 `https://agenterm.net/mcp`。[查看配置示例](docs/usage.zh-CN.md#接入-agent)
2. **邀请并授权。** 请 Agent 创建邀请，把它返回的短链接或对应系统的连接命令私下发给对方。短链接适用于启用了短码的单设备邀请。对方在目标电脑打开链接或运行命令并授权。
3. **开始协助。** 设备上线后告诉 Agent 要做什么。对方可在客户端查看命令和输出，并随时断开。

连接授予目标电脑当前用户的终端权限，请只向信任的人授权。Agent 和客户端经服务端交换命令与结果，目标电脑无需开放入站端口。托管服务开放创建邀请，并按邀请创建者的 IP 限制可同时连接的设备数。

当前源码使用 Rust CLI。公开预发布版 [v0.10.17](https://github.com/Heming9/agenterm/releases/tag/v0.10.17) 仍附带 Go 客户端。托管 MCP 和发布版的实际工具字段以各自的 `tools/list` 为准。

[使用指南与排障](docs/usage.zh-CN.md) · [自托管](docs/deployment.zh-CN.md) · [MCP 工具参考](docs/mcp-tools.zh-CN.md) · [问题反馈](https://github.com/Heming9/agenterm/issues) · [Apache-2.0](LICENSE)
