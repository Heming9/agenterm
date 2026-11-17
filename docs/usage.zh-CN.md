# 使用指南

[English](usage.md) · [返回首页](../README.zh-CN.md) · [自托管](deployment.zh-CN.md)

## 接入 Agent

### 🤖 让 Agent 帮你配置

将下面的内容发给你的 Agent：

```text
请帮我把 Agenterm 配置为当前 Agent 的 MCP 服务，使用 Streamable HTTP，地址是 https://agenterm.net/mcp。请保留已有配置，完成后检查连接状态，并告诉我是否需要重启或重新加载。
```

### ⚙️ 手动配置

以下示例使用 Agenterm 托管服务。已有配置文件时，只需合并对应的 `agenterm` 条目，不要覆盖其他配置。

**Codex**：在终端运行（适用于 CLI 和 IDE 扩展）：

```sh
codex mcp add agenterm --url https://agenterm.net/mcp
```

**Claude Code**：在终端运行，添加到当前用户的所有项目：

```sh
claude mcp add --transport http --scope user agenterm https://agenterm.net/mcp
```

**Gemini CLI**：在终端运行，添加到用户配置：

```sh
gemini mcp add --transport http --scope user agenterm https://agenterm.net/mcp
```

添加后重新加载 MCP 配置或重启 Agent，并在其 MCP 设置中确认连接状态。其他支持 Streamable HTTP 的 Agent 也可以使用相同地址。

自行部署时，使用服务端显示的 MCP 地址（默认路径为 `/agenterm/mcp`）。将管理密钥作为 Streamable HTTP 的 Bearer 凭据，客户端会发送 `Authorization: Bearer <管理密钥>`。例如，先在运行 Codex 的环境中设置 `AGENTERM_MANAGEMENT_KEY`，再注册该地址：

```sh
codex mcp add agenterm --url https://YOUR_HOST/agenterm/mcp --bearer-token-env-var AGENTERM_MANAGEMENT_KEY
```

重启后，确认 Agent 进程能读取该环境变量。请妥善保管密钥。

## 🤝 开始远程协助

1. 告诉 Agent 要完成的任务，请它为目标电脑创建邀请。
2. 私下把返回的短链接或目标系统的连接命令发给对方。对方在目标电脑打开链接或运行命令，阅读提示并授权。
3. 保持 Agent 对话进行。设备上线后开始任务，完成后断开连接。

短链接只适用于启用了短码的单设备邀请，自托管默认关闭短码。需要协助多台电脑时，请 Agent 创建多设备邀请，并在每次操作前指定目标设备。

## 💻 客户端操作

当前源码使用 [Rust CLI](../tools/agenterm-cli/README.md)。常规启动会注册 `agenterm-cli:` 协议。注册后，可在邀请页选择「打开终端版并连接」，再选择临时连接或持久连接。首次使用或浏览器无法打开客户端时，运行页面提供的系统命令，或把连接令牌或完整短码地址粘贴到客户端。

裸短码需要已有服务器历史。输入短码后选择签发它的服务器；没有历史时粘贴完整地址。对于 HTTPS 地址，保留 `https://` 前缀。

临时连接在空闲 5 分钟后自动断开，运行命令的时间不计入空闲。持久连接没有此空闲时限。直接启动 CLI 时可用 `-connection-mode=temporary` 或 `-connection-mode=persistent` 指定模式，默认为临时连接。邀请页和安装命令采用授权时选定的模式。

使用方向键或滚轮浏览输出，拖选文字复制。没有活动选区时，按 Ctrl+C 断开并返回连接界面。在连接界面中，Ctrl+C 先清空令牌，令牌为空时退出。

一键连接下载的客户端会保留在本机缓存中；关闭客户端后可以删除：

| 系统 | 缓存目录 |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

## ❓ 常见问题

### 连接失败或邀请过期

确认双方都能访问服务地址。`127.0.0.1` 只能从服务端所在电脑访问。出现版本不兼容时，使用与服务端同一发行版的客户端。若单设备邀请提示已有连接，请先关闭另一客户端。

默认有 15 分钟完成首次连接。设备在线时，已有连接继续有效。最后一台设备断开后，设备令牌和仍可用的短码还有 3 分钟可用于重新连接。短码在第 10 次成功兑换后失效，浏览页面不计入次数。凭据无法使用时，请 Agent 重新创建邀请。

### 网络断开后怎么办

客户端会尝试重连。同一客户端进程可以继续使用仍保留的终端和输出。请让 Agent 检查设备和任务状态再继续，已执行的命令不会自动重放。保持 Agent 对话进行，设备重新上线时才能继续协助。

### 支持远程桌面吗？

Agenterm 提供终端协助，不提供远程桌面。Windows 支持简单的交互输入；需要真实终端的全屏程序可能无法使用。

### 如何传输文件？

在目标设备通过 Rust CLI 上线后，可通过 MCP 的 `p2p_transfer_start` 发起文件传输。两台设备需要能建立直连，每次传输一个文件。跨设备信令要求 HTTPS，HTTP 仅用于回环地址。关闭客户端会停止其 P2P 组件。详见 [P2P 组件说明](../tools/p2p-cli/README.md)。

### 权限与隐私

连接授予目标电脑当前用户的终端权限，仅向信任的人授权。Agent、服务端和目标客户端都能看到命令和输出；请私密保管邀请与令牌，避免通过会话传递密码等敏感信息。断开连接不能撤销已经完成的操作；在不可信网络中请使用 HTTPS。

开发者可查阅 [MCP 工具技术参考](mcp-tools.zh-CN.md)。
