# Agenterm

<img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm 应用图标" width="128">

[English](README.md) | 简体中文

让本机 Agent 通过 MCP 在另一台电脑执行命令。支持多个 Client 同时在线，每个 Client 使用独立的 token。

```text
Agent ── HTTP / MCP ──> Server <── WebSocket ── Client ──> shell
```

## 安全范围

**Server 默认使用未加密的 HTTP 和 WebSocket。能监听流量的人可能读取 token、命令及输出；持有 token 的人可以在对应 Client 上执行命令，权限等同于运行 Client 的用户。仅在可信网络或受控测试环境使用，不要直接暴露到公网。** 每个 token 用于验证并选择对应的 Client；token 可重复使用，Server 重启后失效。Client 不会保存 token 或自动重连。

认证失败按直接连接的来源 IP 计数：1 分钟内失败 10 次后，该 IP 被限制 5 分钟；限制期间正确凭证也不能使用。共享 IP 的用户会相互影响。Server 不信任转发头：使用反向代理时，所有请求可能显示为同一个代理 IP，建议同时在代理入口按真实来源 IP 限流。分布式来源仍可继续尝试。

Server 可以看到命令与输出。命令会显示在 Client 终端，包含在命令中的敏感信息也可能被显示。关闭 Client 会断开连接并尝试停止运行中的命令，但已完成的操作不能撤销，脱离的子进程也不一定会停止。

## 构建

需要 Go 1.24.3 或更高版本：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
```

构建命令行程序：

```sh
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

为被控电脑的系统和架构编译 Client，例如：

```sh
GOOS=linux GOARCH=amd64 go build -o agenterm-client-linux-amd64 ./cmd/agenterm-client
GOOS=windows GOARCH=amd64 go build -o agenterm-client-windows-amd64.exe ./cmd/agenterm-client
```

## 图形界面

图形版与命令行版使用相同的 Client 内核，采用跟随系统明暗模式的黑白主题。顶部一行放置 Token 输入框和连接按钮；连接后显示服务器地址、连接状态和“断开连接”。执行记录显示命令和实时输出；右上角统计本次连接已完成的命令数与平均耗时，包含失败或取消的命令，不包含仍在执行的命令。“清空”只清除显示的输出，重新连接成功时重置统计。连接过程中可点击“取消连接”，关闭窗口会断开连接。图形版不会保存 token，也不会自动重连。执行记录仅保存在内存，最多保留最近约 64 KiB；命令或输出可能含有敏感信息。

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
```

GUI 使用 Fyne，构建需要 CGo 和本机 C 编译器。Linux 还需要相应图形库开发文件；跨平台编译需要目标系统的 C 交叉编译器，不能直接复用下面纯 Go 命令行版的交叉编译方式。Windows 可在本机用 `go build -ldflags=-H=windowsgui -o Agenterm.exe ./cmd/agenterm-gui` 构建，避免额外的控制台窗口。不同系统的图形包依赖和发布要求参见 [Fyne 构建说明](https://docs.fyne.io/started/)。

要打包包含系统应用图标的 GUI，请先安装与项目 Fyne 版本一致的 CLI，再在目标系统上运行对应的打包命令：

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
cd cmd/agenterm-gui
fyne package -os darwin --use-raw-icon   # macOS：Agenterm.app
fyne package -os windows -icon assets/app-icon-windows.png  # Windows：Agenterm.exe
```

选择当前操作系统对应的命令；Windows 打包需要可用的 Windows C 编译器。Fyne CLI 每次打包后会递增 `FyneApp.toml` 中的构建号。

## 运行

在 Server 机器启动服务：

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

将 `SERVER_IP` 改为 Client 和 Agent 都能访问的地址。Server 默认输出 5 个 `agenterm:<编码内容>` 格式的连接 token 和 MCP 地址；可用 `-clients N` 设置签发数量（1–1000）。每个 token 对应一条 Client 链路，同一个 token 同时只能连接一个 Client。编码内容采用紧凑二进制结构，包含连接协议、16 字节（128 位）随机凭证和主机地址（含可选端口），使用无填充的 Base64 URL-safe 编码；连接路径固定为 `/client`。IPv4/IPv6 地址及其端口使用二进制存储，域名保留为文本。默认本机地址的 token 为 40 个字符。此格式不兼容旧版 token，Server 与 Client 需同时更新。默认仅监听 `127.0.0.1:8080`，适合单机测试。这里的 token 包含 Server 地址和接入凭证，按密码保管。

在被控电脑运行：

```sh
./agenterm-client
```

按提示粘贴 token 并回车。Client 不安装系统 SSH 服务，也不要求单独的系统登录；它以当前登录用户的权限运行命令。

### 使用一次性邀请连接

使用发布版 Server 时，每个连接 token 同时生成一组安装命令，分别供 macOS/Linux 和 Windows PowerShell 使用。将适合对方系统的命令私下发送给对方；命令从该 Server 对应版本的 GitHub Release 下载脚本和客户端，自动检测架构。macOS arm64 和 Windows amd64 使用图形客户端，其他已支持的 macOS、Linux 和 Windows 架构使用终端客户端。客户端展示服务器地址与权限说明，对方确认后才会连接。关闭客户端即可断开；不会设置开机自启。

邀请有效期为 15 分钟、只能兑换一次。过期后在 Server 控制台按回车，可为所有 token 重新生成邀请，并作废尚未兑换的旧邀请；已有连接不受影响。每个 token 仍只能同时连接一个客户端，兑换后的连接 token 原有的有效期规则不变。邀请命令及 token 都是凭证，应私下发送，避免粘贴到公开场所；命令可能保存在终端历史中，完成后应清理。

对方需要访问 GitHub 和 Server 的 `-public-url`；默认的 `127.0.0.1` 只适用于同机。生产环境应通过支持 WebSocket 的 HTTPS 反向代理提供 Server，防止邀请及连接凭证被窃听。脚本使用 Server 内嵌的脚本 SHA-256 和 Release 的校验清单检查下载；后者用于发现下载损坏，不代替代码签名或保护已被篡改的 Release。源码本地构建不会生成下载命令，请使用发布版。

在支持 Streamable HTTP 和自定义请求头的 Agent 中添加远程 MCP 服务，地址为 `http://SERVER_IP:8080/mcp`。将请求头 `Authorization` 设置为 `Bearer <token>`，其中 token 是目标 Client 使用的完整 `agenterm:` token。需要操作多个 Client 时，可分别配置 MCP 服务；不同 Agent 对自定义请求头的支持可能不同。连接后调用 `execute_command`，传入 `command` 参数。例如：

```json
{"command":"pwd && ls"}
```

`execute_command` 适合非交互式命令，在命令结束后返回输出和退出码。交互式命令使用以下 MCP 工具，Agent 可以在命令运行期间逐段读取输出并回复提示：

1. `session_start`：传入 `command`，取得 `session_id`。
2. `session_read`：传入 `session_id`；使用返回的 `next_cursor` 作为下次的 `cursor`，设置 `wait_ms`（最多 20000 毫秒）等待新输出。`done` 为 true 时带有 `exit_code`。
3. `session_write`：传入 `session_id` 和 `input`；回答提示时通常需要包含换行符。
4. `session_stop`：停止会话。

实时输出通过 Agent 多次调用 `session_read` 获取，而非自动推送；具体展示效果取决于 Agent。Client 本地也会显示命令及输出。每个会话保留最近 1 MiB 输出，落后太多的读取会返回 `truncated: true`。已结束会话保留至少 30 分钟，在新建会话时清理。

MCP 客户端必须能访问 Server；不同 Agent 的配置方式和远程 MCP 支持情况不同。

直接检查 MCP 接口：

```sh
curl -s http://127.0.0.1:8080/mcp \
  -H 'Authorization: Bearer YOUR_AGENTERM_TOKEN' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_command","arguments":{"command":"echo hello"}}}'
```

Server 当前只提供 HTTP。使用 HTTP 的 `-public-url` 时会输出未加密警告。跨设备时可在可信网络中使用；如需加密传输，可在前面配置支持 WebSocket 的 HTTPS 反向代理，并将 `-public-url` 设为代理的 HTTPS 地址。代理与 Server 同机时，建议仅监听本机，例如 `-listen 127.0.0.1:8080 -public-url https://agenterm.example.com`。仅设置 HTTPS 地址不会自动启用 TLS，必须单独配置反向代理。HTTPS 可以保护传输，但仍需妥善保管 token；公开部署还需要适当的访问控制。

## 当前限制

- macOS/Linux 交互会话使用 PTY；Windows 使用普通输入输出管道，支持简单提示交互，但不支持要求真实终端的程序。暂不支持远程图形界面。
- 全屏终端界面会输出控制字符，Agent 不一定能正确理解。终端输入可能被程序回显，避免通过 Agent 输入密码等敏感信息。
- 每个 token 同时只允许一个 Client 连接；Server 重启后 token 失效，需要使用新 token 重新连接。连接中断后也需要重新连接。
- `execute_command` 在结束后返回输出；需要边运行边读取时使用 `session_start` 和 `session_read`。
- Windows 输出会根据首段非 ASCII 内容推测 UTF-8 或系统代码页并转换为 UTF-8；同一命令混合多种编码时仍可能乱码。
- 命令执行使用 macOS/Linux 的 `/bin/sh -c` 或 Windows 的 `cmd.exe /C`。

## 开源协议

本项目采用 [Apache License 2.0](LICENSE) 开源协议。
