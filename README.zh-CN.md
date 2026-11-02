<div align="center">
  <img src="cmd/agenterm-client-gui/assets/app-icon-agent.png" alt="Agenterm" width="96">
  <h1>Agenterm</h1>
  <p><strong>通过一条授权命令，让 AI Agent 协助操作远程电脑。</strong></p>
  <p>macOS / Linux / Windows · CLI / GUI · 多设备 · 无需 SSH 服务</p>
  <p>
    <a href="#快速开始">快速开始</a> ·
    <a href="https://github.com/Heming9/agenterm/releases">下载客户端</a> ·
    <a href="docs/deployment.zh-CN.md">自托管</a> ·
    <a href="README.md">English</a>
  </p>
</div>

<p align="center">
  <a href="docs/demo/agenterm-cleanup.mp4"><img src="docs/demo/quickstart.gif" alt="Agent 发出邀请，目标电脑授权连接，随后执行任务" width="800"></a>
</p>
<p align="center">生成邀请 → 确认授权 → 设备上线 → 执行任务</p>
<p align="center"><a href="docs/demo/agenterm-cleanup.mp4">完整演示 · 1 分 42 秒</a> · <a href="docs/demo/subtitles.zh-en.srt">中英字幕</a></p>

## 快速开始

<p align="center">
  <img src="docs/design/connection-flow-zh.gif" alt="使用流程：让 Agent 接入 MCP，生成连接命令，分享连接命令，对方执行并授权连接，Agent 开始远程协助" width="1000">
</p>

### 1. 发起协助的人：接入 Agent

在支持 Streamable HTTP 的 Agent 中添加 MCP 服务：

```text
https://agenterm.net/mcp
```

[查看配置示例](docs/usage.zh-CN.md#接入-agent)。接入后，告诉 Agent 要完成的任务，并让它创建连接邀请，将生成的一键命令私下发送给目标电脑的使用者。

### 2. 目标电脑的使用者：连接并授权

运行适合自己系统的邀请命令，阅读提示并确认授权。客户端会显示连接状态，以及 Agent 执行的命令和输出；无需配置 MCP 或 Server。

### 3. 双方：完成任务并断开

保持 Agent 对话进行，等待设备上线后开始任务。目标电脑可随时断开；任务结束后关闭客户端。

邀请默认单次使用、5 分钟有效。调用 `create_client` 时可通过 `timeout_seconds` 自定义邀请有效期，单位秒。连接授予当前用户的终端操作权限，请只向信任的人分享邀请和 token。

## 适合哪些任务

- **排查开发环境：**“帮同事检查 Python 环境，定位项目启动失败的原因。”
- **检查系统状态：**“查看这台服务器的磁盘占用和应用日志。”
- **协作检查多台电脑：**“连接几台测试机器，分别检查运行环境并汇总结果。”

支持交互式终端任务。目标电脑可以选择 CLI 或 GUI 查看执行过程，临时网络故障后客户端会自动尝试重连。

## 工作原理

<p align="center">
  <img src="docs/design/architecture-blue.gif" alt="Agenterm 架构：AI Agent 通过 MCP 接入 Server，经 WebSocket 向 Client 和本地 shell 下发命令并接收结果" width="1000">
</p>

只需启动 Server，它会自动管理内部 Runtime：Runtime 保留连接与内存会话，Server 提供 MCP 和下载接口并负责 Runtime 的启动、接管和停止。Client 在目标电脑运行并主动连接 Server，再由本地 shell 执行任务。Agent 和目标电脑都需要能访问 Server，目标电脑无需开放入站端口。

可以使用托管服务，也可以[部署自己的 Server](docs/deployment.zh-CN.md)。

## 常见问题

<details>
<summary>连接失败或邀请过期怎么办？</summary>

确认双方都能访问 Server；邀请过期、已使用或 Runtime 重启后，让 Agent 重新创建邀请。出现版本不兼容提示时，使用同一 Release 的 Server 和客户端。[连接排查](docs/usage.zh-CN.md#连接失败或邀请过期)。

</details>

<details>
<summary>断线后会自动继续任务吗？</summary>

客户端会尝试重连，但不会自动重放命令。让 Agent 重新确认设备和任务状态；已经结束的 Agent 对话不会因设备上线而自动唤醒。

</details>

<details>
<summary>能操作远程桌面或连接多台电脑吗？</summary>

Agenterm 提供终端协助，不提供远程桌面。多设备任务可让 Agent 创建多设备邀请；Windows 支持简单输入交互，要求真实终端的全屏程序可能无法使用。

</details>

<details>
<summary>托管服务有什么限制？</summary>

目前无需管理认证，每个邀请创建者的公网 IP 最多允许 4 台在线设备，同一网络共享额度。Agent、Server 和目标客户端都能看到命令与输出，请妥善保管邀请和 token。自行部署到公网时使用 HTTPS。

</details>

[使用指南](docs/usage.zh-CN.md) · [部署与源码构建](docs/deployment.zh-CN.md) · [问题反馈](https://github.com/Heming9/agenterm/issues) · [Apache-2.0](LICENSE)
