# Agenterm

<img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm 应用图标" width="128">

[English](README.md) | 简体中文

**让Agents随时随地控制任意远程电脑**

```text
Agent ── HTTP / MCP ──> Server <── WebSocket ── Client ──> shell
```

macOS / Linux / Windows · CLI / GUI · 多客户端 · 交互式会话 · 无需 SSH 服务

## 快速开始

从 [Releases](https://github.com/Heming9/agenterm/releases) 下载，或用 Go 1.24.3+ 编译：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

**1. 启动 Server。** 以下命令用于可信网络：

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080 --generate
```

将 `SERVER_IP` 换成 Agent 和目标电脑都能访问的地址，复制输出的 `agenterm:` token。

**2. 连接目标电脑。** 运行 Client，粘贴 token：

```sh
./agenterm-client
```

发布版 Server 还会输出 macOS/Linux 和 Windows PowerShell 的一键安装命令。私下发送对应命令，对方确认授权后连接。

**3. 接入 Agent。** 添加支持 Streamable HTTP 和自定义请求头的远程 MCP 服务：

```text
URL:            http://SERVER_IP:8080/agenterm/mcp
Authorization:  Bearer agenterm:...
```

使用与 Client 相同的完整 token，然后调用 `execute_command`：

```json
{"command":"pwd && ls"}
```

> token 等于目标电脑当前用户的 shell 权限。默认 HTTP/WS 不加密；请在可信网络使用，暴露到公网前先配置 HTTPS 反向代理。[安全细节见后文](#详细说明)。

## MCP 工具

| 工具 | 用途 |
| --- | --- |
| `create_client` | 创建 token 和一次性邀请，需要管理权限。 |
| `wait_for_client` | 等待目标电脑上线。 |
| `execute_command` | 执行命令，结束后返回输出和退出码。 |
| `session_start` | 启动交互式命令，返回 `session_id`。 |
| `session_read` | 用 `session_id` 和 `cursor` 读输出，下次传入返回的 `next_cursor`。 |
| `session_write` | 向会话发送 `input`，提交一行时带上换行符。 |
| `session_stop` | 停止会话。 |

管理多台电脑时，用 Server 输出的**管理密钥**接入 MCP。调用 `create_client`（参数 `{}`），向对方展示返回的 `display_text`，后续工具调用通过 `client_token` 选择目标：

```json
{"client_token":"agenterm:...","timeout_ms":90000}
```

不加 `--generate` 时，Server 只按需创建连接。Client token 控制单个目标，管理密钥控制所有目标。

## 客户端

- **CLI：**默认进入终端界面。方向键滚动，`c` 清空日志，`q` / Ctrl+C 断开退出。`-plain` 使用纯文本输出；重定向输入或输出时自动启用。
- **GUI：**粘贴 token、连接、查看命令和实时输出。跟随系统明暗主题，当前界面为中文。[构建方法见后文](#详细说明)。

两种客户端都只在内存保留最近约 64 KiB 日志，不保存 token，断线后需手动重连。关闭客户端即断开连接。

同一系统账户下，CLI 和 GUI 对每个服务器地址共用一把本机连接锁，即使使用不同邀请也只能保留一条连接。重复连接会在兑换邀请前退出；断开连接或进程退出后自动释放锁。不同服务器地址可分别连接。锁文件位于 `~/.agenterm/locks`，不含连接凭据，与下载缓存分开存放；客户端运行期间请勿删除，文件存在本身不会阻止连接。此检测需要使用新版客户端，不合并指向同一服务器的不同域名别名。

## 详细说明

<details>
<summary>邀请与下载</summary>

发布版 Server 提供六种系统与架构的 CLI 压缩包及校验清单。一键命令从 Server 下载同版本安装脚本，首先提示远程控制警告并请求授权；确认后检查邀请有效性、版本兼容性和 `/agenterm/client` 路径的 WebSocket 握手，再校验缓存或下载当前平台的 CLI 并自动连接，不再重复确认。取消则立即结束，不进行探测或下载客户端。探测不会消耗邀请，也不会使客户端上线。授权确认后，安装过程显示 4 个连接阶段：连通性检查、准备客户端、建立连接、连接成功；终端下载显示传输进度，命中有效缓存时跳过下载。不安装开机自启服务。GUI 从 Release 单独下载；源码构建的 Server 不提供下载命令。

命令使用 `/agenterm/c/<22位邀请ID>` 短入口，Windows 入口带 `.ps1` 后缀。Server 动态返回带好邀请、版本和下载地址的安装脚本。入口 ID 复用原有 128 位随机邀请凭证；获取脚本不会消耗邀请，响应禁止缓存。入口 URL 等同于邀请，应私下分享并避免记录到代理访问日志。

邀请 15 分钟有效，只能兑换一次。在 Server 控制台按回车可替换启动时生成的未用邀请，不影响已有连接或 MCP 创建的邀请。兑换后的 Client token 可重复使用，Server 重启后失效，每个 token 同时只允许一个 Client 在线。凭证应私下分享；邀请命令可能留在 shell 历史中。

目标电脑只需访问 `-public-url`，连接和下载过程均无需访问 GitHub；`127.0.0.1` 仅适用于同机。反向代理只需统一转发 `/agenterm/`，保留此前缀，并允许 WebSocket 升级及 Authorization 请求头。探测失败时停止下载；探测成功后，每次运行都会从 Server 获取校验清单并校验缓存的 CLI，缺失或损坏时重新下载。缓存按版本、系统和架构存放：

| 系统 | 缓存目录 |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

断开后缓存保留，可手动删除；每次连接仍需授权。安装脚本和下载资源使用 Server 的 HTTP(S) 地址，不校验脚本的固定哈希；公网部署请使用 HTTPS。校验和用于发现下载损坏，不能替代代码签名，也不能防御 Server 或发布包本身被篡改。macOS/Linux 使用 gzip 压缩包，Windows 使用 zip；Server 在接受连接前于内存中准备好下载包，仍由目标电脑解压。

</details>

<details>
<summary>认证与部署</summary>

Server 默认监听 `127.0.0.1:8080`，每次启动生成 10 位小写字母和数字组成的管理密钥。公网部署请用 `--passwd` 设置更长的随机密钥：

```sh
./agenterm-server -listen 127.0.0.1:8080 \
  -public-url https://agenterm.example.com \
  --passwd '替换为足够长的随机密钥'
```

另行配置支持 WebSocket 的 HTTPS 反向代理。`-public-url` 可填域名根地址或以 `/agenterm` 结尾的地址，输出端点始终使用 `/agenterm/` 前缀；它只声明对外地址，不会启用 TLS。代理与 Server 同机时，后端仅监听本机。公网部署还需适当的访问控制。

MCP 接受 `Authorization: Bearer <管理密钥>` 或 `Authorization: Bearer <Client token>`。管理模式用参数 `client_token` 选择目标；普通 Client token 无权调用 `create_client`。也可为每个目标单独配置 MCP。Agent 需支持 Streamable HTTP 和自定义请求头。

`create_client` 返回 `client_token`、`invitation`、`expires_at` 和 `display_text`。发布版还返回 `macos_linux_command` 与 `windows_powershell_command`，两条命令共用同一个邀请，任选其一。Server 最多创建 1000 个 Client token；即使管理密钥固定，所有 token 和邀请仍在重启后失效。

`-no-password` 关闭 MCP 管理认证，任何能访问接口的人都能创建和控制连接，仅限私有环境。不能与 `--passwd` 同用。

版本号遵循 [SemVer 2.0.0](https://semver.org/lang/zh-CN/)，连接时双向校验，并采用以下兼容策略：

- `1.0.0` 及以后的稳定版本：主版本相同即可连接，次版本和补丁版本可以不同。同一主版本内，新增功能必须保留旧通信行为。
- `0.x`：SemVer 不承诺开发阶段的 API 稳定性，因此 Agenterm 约定主、次版本必须一致；`0.0.x` 还要求补丁号一致。
- 预发布版本（`alpha/beta/rc` 等）：只有版本核心及预发布标识完全相同才允许连接，不与稳定版互通。
- 构建元数据（`+build` 等）不影响兼容性；版本信息缺失或格式错误会被拒绝。

这些检查覆盖安装探测、邀请兑换、CLI 和 GUI 正式连接，失败不会消耗邀请或登记客户端上线。

Client token 包含 Server 地址和 128 位随机凭证。所有接口统一使用 `/agenterm/` 前缀，旧根路径接口和旧 token 不再支持。Server 和 Client（包括 GUI）需一起更新，并将 MCP 地址改为 `/agenterm/mcp`。

</details>

<details>
<summary>会话行为与平台限制</summary>

- `create_client` 成功后，Agent 必须先展示 `display_text`，再使用返回的 `client_token` 调用 `wait_for_client`；仅在返回 `{"status":"connected"}` 后执行命令或启动会话。`wait_for_client` 也可能返回 `{"status":"timeout"}`。`timeout_ms` 默认 90000（90 秒），范围 0–300000，0 表示立即检查。Agent 宿主需允许对应时长，超时后可按需再次调用。调用顺序通过工具描述提示，不由服务端强制校验。
- `session_read` 通过轮询获取输出，`wait_ms` 最多等待 20000 毫秒；`done: true` 时返回 `exit_code`。每个会话保留最近 1 MiB 输出，读取落后时返回 `truncated: true`。已结束会话保留至少 30 分钟，新建会话时清理。
- macOS/Linux 会话使用 PTY。Windows 使用管道，支持简单提示交互，不支持要求真实终端的程序。暂不支持远程图形界面。
- 全屏终端程序输出的控制序列可能无法被 Agent 正确理解。CLI 会过滤本地日志中的控制序列，它是日志查看器。输入可能被回显，避免通过 Agent 输入敏感信息。
- 命令使用直接启动 Client 的受支持 shell（PowerShell、cmd、bash、zsh、fish 等）。无法识别时，macOS/Linux 回退到 `/bin/sh`，Windows 回退到 `ComSpec`（通常是 `cmd.exe`）。每条命令启动新的 shell，不共享启动 shell 的会话变量和函数。
- Windows 根据首段非 ASCII 内容识别 UTF-8 或系统代码页；混合编码仍可能乱码。
- 断线后需手动重连；Server 重启后需获取新 token。
- GUI 统计已完成命令数和平均耗时，包含失败或取消的命令。清空日志只影响显示，重连成功后重置统计；连接过程中可取消。

</details>

<details>
<summary>安全模型</summary>

Server 可见命令与输出，Client 也会在本地显示，两者都可能包含敏感信息。关闭 Client 会尝试停止运行中的命令，但无法撤销已完成操作，也不能保证停止脱离的子进程。

默认 HTTP 和 WebSocket 不加密，流量监听者可读取凭证、命令和输出。不可信网络使用 HTTPS，并保管好 token 和管理密钥。

认证按直连来源 IP 限流：1 分钟内失败 10 次后封禁 5 分钟，期间正确凭证也会被拒绝。共享 IP 的用户相互影响。Server 忽略转发头；反向代理场景还应在代理入口按真实来源 IP 限流。分布式来源仍可继续尝试认证。

</details>

<details>
<summary>交叉编译与 GUI 打包</summary>

CLI 交叉编译：

```sh
GOOS=linux GOARCH=amd64 go build -o agenterm-client-linux-amd64 ./cmd/agenterm-client
GOOS=windows GOARCH=amd64 go build -o agenterm-client-windows-amd64.exe ./cmd/agenterm-client
```

Fyne GUI 需要 CGo 和 C 编译器；Linux 还需图形开发库，跨平台编译需目标平台的 C 工具链。参见 [Fyne 构建说明](https://docs.fyne.io/started/)。

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
# Windows：隐藏控制台窗口
go build -ldflags=-H=windowsgui -o Agenterm.exe ./cmd/agenterm-gui
```

打包带图标的应用，先安装匹配版本的 Fyne CLI，再在目标系统执行对应命令：

```sh
go install fyne.io/fyne/v2/cmd/fyne@v2.8.2
cd cmd/agenterm-gui
fyne package -os darwin --use-raw-icon   # Agenterm.app
fyne package -os windows -icon assets/app-icon-windows.png  # Agenterm.exe
```

Windows 打包需要可用的 Windows C 编译器。打包会递增 `FyneApp.toml` 的构建号。

发布 CI 将六个平台的原始 CLI 程序合并为一个 XZ 压缩的 tar 包并内嵌到 Server。Server 启动时通过纯 Go 解码器，在打印邀请和开始监听前，于内存中重建标准 gzip／ZIP 下载包和 SHA256SUMS。这以启动时的 CPU 和内存开销换取更小的 Server 程序，服务端和目标电脑均无需额外工具。发布打包工具与 Server 共用确定性的归档生成逻辑，并使用同一固定版本的 Go 工具链；CI 逐字节比对重建的全部下载包和校验清单，禁止单独重压缩发布的 CLI 包。工作流摘要会报告产物大小。GUI 图标采用无损重压缩，尺寸、像素和元数据保持一致。

</details>

<details>
<summary>用 curl 检查 MCP</summary>

```sh
curl -s http://127.0.0.1:8080/agenterm/mcp \
  -H 'Authorization: Bearer YOUR_AGENTERM_TOKEN' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_command","arguments":{"command":"echo hello"}}}'
```

</details>

## 测试

```sh
go test ./...
```

## 协议

[Apache-2.0](LICENSE)

## 语言

桌面客户端默认跟随系统语言。可在应用菜单的 **Language / 语言** 中选择英文、简体中文或跟随系统；重启应用后生效。终端客户端和服务端跟随终端语言环境。不支持的语言统一回退为英文。可设置 `AGENTERM_LANG=en` 或 `AGENTERM_LANG=zh-CN`，覆盖终端进程及其安装脚本的语言。命令输出保持原样。

所有应用文案统一维护在 `internal/i18n/messages.json`，使用稳定的 key，提供 `en` 和 `zh-CN` 两种翻译。Go 代码引用 key；安装脚本由服务端从同一资源注入翻译。新增文案时请在此补齐两种语言，并保持格式参数一致。
