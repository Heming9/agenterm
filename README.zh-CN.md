<div align="center">
  <h1>Agenterm</h1>
  <img src="cmd/agenterm-gui/assets/app-icon-agent.png" alt="Agenterm 应用图标" width="128">
  <p><a href="README.md">English</a> | 简体中文</p>
  <p><strong>让Agents随时随地控制任意远程电脑</strong></p>
  <p>通过一键连接远程电脑，让 AI Agent 使用 MCP 和终端提供远程协助。</p>
  <p><code>Agent ── HTTP / MCP ──&gt; Server &lt;── WebSocket ── Client ──&gt; shell</code></p>
  <p>macOS / Linux / Windows · CLI / GUI · 多客户端 · 交互式会话 · 无需 SSH 服务</p>
</div>

## 使用托管服务

无需自行部署 Server，可将支持 Streamable HTTP 的 Agent 连接到：

```text
https://agenterm.net/mcp
```

托管服务的管理接口目前无需认证。以 Codex 为例，在 `~/.codex/config.toml` 中添加：

```toml
[mcp_servers.agenterm]
url = "https://agenterm.net/mcp"
```

重启 Agent 或重新加载 MCP 配置后：

1. 调用 `create_client`，参数为 `{}`。向目标电脑的使用者**私下发送**返回的 `display_text` 中适合其系统的一键连接命令。
2. 对方在自己的电脑上运行命令，阅读提示并确认远程控制授权。macOS/Linux 和 Windows 命令使用同一个邀请，任选其一；邀请 15 分钟内有效且只能兑换一次。
3. 使用返回的 `client_token` 轮询 `list_clients`；设备出现在 `clients` 数组后，将同一个 `client_token` 传给 `execute_command` 或会话工具，例如：

   ```json
   {"client_token":"agenterm:...","command":"pwd && ls"}
   ```

托管服务按**创建邀请的请求来源 IP**限制为最多 4 个在线客户端；同一公网 IP 的使用者共享额度。名额未满时，创建新邀请可能使最早的未使用邀请失效。Server 重启后已有 token 和邀请会失效，需要重新创建。

> 托管服务是公共管理接口，任何可以访问接口的人都能创建自己的客户端；持有某台目标电脑的 client token 即可控制该电脑。请私密保管 token 和邀请，只在取得对方授权后连接。如需自行控制管理访问，请使用下方的自托管方式。

## 自托管快速开始

从 [Releases](https://github.com/Heming9/agenterm/releases) 下载，或用 Go 1.26.10+ 编译：

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

发布版 Server 还会输出 macOS/Linux 和 Windows CMD / PowerShell 的一键安装命令。私下发送对应命令，对方确认授权后连接。

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
| `create_client` | 创建 token 和邀请；`multi_device: true` 开启多设备模式。需要管理权限。 |
| `list_clients` | 列出当前 token 下所有在线设备，供 Agent 轮询。 |
| `execute_command` | 执行命令，结束后返回输出和退出码。 |
| `session_start` | 启动交互式命令，返回 `session_id`。 |
| `session_read` | 用 `session_id` 和 `cursor` 读输出，下次传入返回的 `next_cursor`。 |
| `session_write` | 向会话发送 `input`，提交一行时带上换行符。 |
| `session_stop` | 停止会话。 |

管理多台电脑时，用 Server 输出的**管理密钥**接入 MCP。调用 `create_client`（参数 `{}`），向对方展示返回的 `display_text`，后续工具调用通过 `client_token` 选择目标：

```json
{"client_token":"agenterm:..."}
```

不加 `--generate` 时，Server 只按需创建连接。Client token 控制其下的设备，管理密钥可选择所有 token。`--generate` 创建的仍是单设备 token。

### 多设备 token

调用 `create_client` 时传入 `{"multi_device":true}`，同一个 token 可让多台设备同时在线；返回的一键连接命令在 15 分钟内可重复使用。省略该参数或传 `false` 时，保持单设备行为。

向目标用户展示 `display_text` 后，使用该 token 轮询 `list_clients`：

```json
{"clients":[{"client_id":"a3k9m2","hostname":"MacBook","platform":"darwin","connected_at":"2026-11-01T00:00:00Z"}]}
```

`client_id` 为 6 位小写字母或数字，服务端检查在线连接间的冲突后分配。主机名仅用于展示，允许重复；旧客户端没有上报信息时主机名和系统为空。设备断线后从列表移除，重连分配新 ID。

多设备模式下，`execute_command` 和 `session_start` 必须提供 `client_id`，即使只有一台设备在线：

```json
{"client_token":"agenterm:...","client_id":"a3k9m2","command":"pwd && ls"}
```

通过不同 `client_id` 并发调用即可并行操作，各设备输出、取消和会话独立。`session_read`、`session_write`、`session_stop` 继续使用 `session_id`，管理模式还需传 `client_token`。单设备 token 可省略 `client_id`；使用 token 直接认证 MCP 时可省略 `client_token`。


## 客户端

- **CLI：**默认进入终端界面。命令和结果超过一屏行时默认折叠为首行预览，点击展开或收起。直接拖选文字，不改变布局和折叠状态；按住鼠标拖到日志区域的上、下边缘可跨屏滚动选择，松开鼠标自动复制，Ctrl+C 可再次复制。点击日志外或按 Esc 清除选区。保留选区期间，日志布局保持稳定，新输出继续接收，清除选区后更新显示。方向键或鼠标滚轮滚动，`c` 清空日志，`q` / Ctrl+C（无选区时）断开退出。SSH 或没有本机剪贴板工具时，通过 OSC 52 请求复制，需要终端支持剪贴板访问。`-plain` 使用纯文本输出；重定向输入或输出时自动启用。
- **GUI：**粘贴 token、连接、查看命令和实时输出。跟随系统明暗主题，当前界面为中文。[构建方法见后文](#详细说明)。

两种客户端都只在内存保留最近最多 10 MiB 日志，不将 token 保存到磁盘。临时连接故障会按 1、2、4、8、16、30 秒的间隔自动重试，之后每 30 秒重试；连接稳定满 60 秒后重置退避。限流时遵守 `Retry-After`，凭据无效或版本不兼容时停止重试。关闭客户端或主动断开会立即取消重试。

服务端在连接建立后立即发送一次 WebSocket Ping，之后每 20 秒发送一次；匹配的 Pong 用于测量往返延迟，不依赖两端时钟同步。GUI 和 TUI 右上角显示最新结果，如“已连接 · 42ms”，每次心跳更新，断线时清除。连续 60 秒未响应则断开连接。旧客户端仍能响应心跳，延迟显示和自动重连需要新版客户端；新版客户端也兼容未声明支持心跳的旧服务端。

自动重连保留历史日志和统计，复用已兑换的 token，并在重试期间持续持有本地连接锁。断线时取消正在执行的命令，不会自动重放；重连后分配新的 `client_id`。服务端日志记录设备 ID、主机名、连接时长和断开原因；客户端日志记录失败原因、重试次数及等待时间，正常心跳不刷日志。

同一系统账户下，CLI 和 GUI 对每个服务器地址共用一把本机连接锁，即使使用不同邀请也只能保留一条连接。重复连接会在兑换邀请前退出；断开连接或进程退出后自动释放锁。不同服务器地址或 URL 前缀可分别连接。锁文件位于 `~/.agenterm/locks`，不含连接凭据，与下载缓存分开存放；客户端运行期间请勿删除，文件存在本身不会阻止连接。此检测需要使用新版客户端，不合并指向同一服务器的不同域名别名。

## 详细说明

<details>
<summary>邀请与下载</summary>

发布版 Server 提供六种系统与架构的 CLI 压缩包及校验清单。一键命令从 Server 下载同版本安装脚本，首先提示远程控制警告并请求授权；确认后检查邀请有效性、版本兼容性和 `/agenterm/client` 路径的 WebSocket 握手，再校验缓存或下载当前平台的 CLI 并自动连接，不再重复确认。取消则立即结束，不进行探测或下载客户端。探测不会消耗邀请，也不会使客户端上线。授权确认后，安装过程显示 4 个连接阶段：连通性检查、准备客户端、建立连接、连接成功；终端下载显示传输进度，命中有效缓存时跳过下载。不安装开机自启服务。GUI 从 Release 单独下载；源码构建的 Server 不提供下载命令。

命令使用 `/agenterm/c/<6位邀请短码>` 短入口，Windows 入口带 `.ps1` 后缀。Server 将短码映射到完整邀请，动态返回带好邀请、版本和下载地址的安装脚本。短码仅使用大写字母 A–Z 和数字 0–9，共 36^6 种组合（约 31 位随机信息），生成时碰撞重试，15 分钟有效，并沿用认证失败限流。短入口比原来的 128 位 URL 更容易被猜中；完整邀请和连接凭证仍保持 128 位随机强度。获取脚本不会消耗邀请，响应禁止缓存。邀请被兑换（单设备模式）、替换或回收时，同时移除短码；仍兼容原有 22 位入口。入口 URL 等同于邀请，应私下分享并避免记录到代理访问日志。

邀请 15 分钟有效，单设备模式只能兑换一次，多设备模式可重复兑换。在 Server 控制台按回车可替换启动时生成的未用邀请，不影响已有连接或 MCP 创建的邀请。MCP 创建的 token 必须在邀请的 15 分钟有效期内首次上线，即使邀请已兑换；上线后可重复使用，最后一台设备断线满 24 小时后回收，重连重新计时，在线期间不会回收。Server 每分钟清理一次过期 token，并同步移除关联邀请、短码和会话记录，创建新邀请时也会执行清理，与是否开启 IP 配额无关。`--generate` 启动 token 保留至 Server 重启，以便控制台重新签发邀请。Server 重启后所有 token 失效；单设备 token 同时只允许一个 Client 在线，多设备 token 允许多台设备同时在线。凭证应私下分享；邀请命令可能留在 shell 历史中。

目标电脑只需访问 `-public-url`，连接和下载过程均无需访问 GitHub；`127.0.0.1` 仅适用于同机。反向代理只需统一转发配置的 URL 前缀（默认 `/agenterm/`），保留此前缀，并允许 WebSocket 升级及 Authorization 请求头。探测失败时停止下载；探测成功后，每次运行都会从 Server 获取校验清单并校验缓存的 CLI，缺失或损坏时重新下载。缓存按版本、系统和架构存放：

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
  --client-ip-header X-Real-IP \
  --passwd '替换为足够长的随机密钥'
```

默认从 TCP 连接的对端地址（`RemoteAddr`，去掉端口）读取客户端 IP，不信任任何转发头。通过反向代理部署时，使用 `--client-ip-header X-Real-IP` 指定读取的请求头；也可以指定其他包含单个 IP 的头。设置参数后只读取该头，缺失、多个值或非法 IP 返回 HTTP 400，不回退到对端 IP。另行配置支持 WebSocket 的 HTTPS 反向代理，并让代理覆盖所选请求头，例如 `proxy_set_header X-Real-IP $remote_addr;`。`-public-url` 可填域名根地址或以配置的前缀结尾的地址；它只声明对外地址，不会启用 TLS。`-url-prefix` 默认 `/agenterm`，统一作用于所有接口、生成的邀请和下载地址；设为 `/`（或空字符串）时使用根路径。前缀必须以 `/` 开头，仅含字母、数字、`-._~` 和 `/`，路径段不可为空、`.` 或 `..`；末尾斜杠会自动移除。例如：

```sh
./agenterm-server -public-url https://agenterm.example.com --client-ip-header X-Real-IP -url-prefix /a --generate
```

此时使用 `/a/mcp`、`/a/client`、`/a/c/<6位短码>` 和 `/a/downloads/...`，反向代理路径与 MCP 地址需同步修改。代理与 Server 同机时，后端仅监听本机。公网部署还需适当的访问控制。

MCP 接受 `Authorization: Bearer <管理密钥>` 或 `Authorization: Bearer <Client token>`。管理模式用参数 `client_token` 选择目标；普通 Client token 无权调用 `create_client`。也可为每个目标单独配置 MCP。Agent 需支持 Streamable HTTP 和自定义请求头。

`create_client` 返回 `client_token`、`multi_device`、`invitation`、`expires_at` 和 `display_text`。发布版还返回 `macos_linux_command` 与 `windows_powershell_command`，两条命令共用同一个邀请，每台设备按其系统选择。即使管理密钥固定，所有 token 和邀请仍在重启后失效。

`-no-password` 关闭 MCP 管理认证，任何能访问接口的人都能创建和控制连接，仅限私有环境。不能与 `--passwd` 同用。

### 按创建者 IP 限制客户端

`--max-clients-per-ip 5` 将每个创建者 IP 的在线客户端限制为 5 个，默认 `0` 表示不限制。配额按调用 MCP `create_client` 的 IP 计算，而不是受邀电脑的 IP。多设备 token 下的每台在线设备都占一个名额。此功能适用于两种认证模式。

在线连接未满时可以创建邀请；在线数加上尚未连接过的 token 数最多为 5。待使用名额已满时，新邀请会撤销该 IP 最早创建且从未连接过的 token 及对应邀请，即使邀请已兑换。从未上线的 token 随原邀请过期回收，包括已兑换但未上线的 token。在线数已满时不能创建；客户端重连时也会检查上限。已连接过的 token 不会因新邀请被回收，但最后一台设备离线满 24 小时后回收；Server 重启后所有 token 失效。`--generate` 在启动时生成的 token 没有创建者 IP，不受此配额限制。

认证限流和客户端配额统一使用同一个 IP 来源：默认使用直连对端 IP；配置 `--client-ip-header <头名>` 后，只使用指定请求头。启用请求头模式时，必须限制 Server 仅供可信反向代理访问，并让代理覆盖该头；否则调用方可以伪造 IP。

Cloudflare → Nginx → Agenterm：Server 设置 `--client-ip-header X-Real-IP`，Nginx 启用 `ngx_http_realip_module`，将 Cloudflare 官方公布的全部 IPv4 和 IPv6 网段设置为 `set_real_ip_from` 并持续更新，设置 `real_ip_header CF-Connecting-IP;`，在 Agenterm 代理位置设置 `proxy_set_header X-Real-IP $remote_addr;`。只信任 Cloudflare 网段，不可信任所有来源。参考 [Cloudflare 还原访客 IP](https://developers.cloudflare.com/support/troubleshooting/restoring-visitor-ips/restoring-original-visitor-ips/) 和 [Cloudflare IP 列表](https://www.cloudflare.com/ips/)。Pseudo IPv4 的覆盖模式会替换原始 IPv6，应关闭以准确计数。IP 配额无法阻止更换 IP，同一 NAT 下的使用者也会共享配额。

版本号遵循 [SemVer 2.0.0](https://semver.org/lang/zh-CN/)，连接时双向校验，并采用以下兼容策略：

- `1.0.0` 及以后的稳定版本：主版本相同即可连接，次版本和补丁版本可以不同。同一主版本内，新增功能必须保留旧通信行为。
- `0.x`：SemVer 不承诺开发阶段的 API 稳定性，因此 Agenterm 约定主、次版本必须一致；`0.0.x` 还要求补丁号一致。
- 预发布版本（`alpha/beta/rc` 等）：只有版本核心及预发布标识完全相同才允许连接，不与稳定版互通。
- 构建元数据（`+build` 等）不影响兼容性；版本信息缺失或格式错误会被拒绝。

这些检查覆盖安装探测、邀请兑换、CLI 和 GUI 正式连接，失败不会消耗邀请或登记客户端上线。

Client token 包含 Server 地址和 128 位随机凭证。默认 `/agenterm` 前缀继续使用原有 token 格式；自定义前缀会写入扩展 token 格式，需要更新 Client（包括 GUI）。Client 自动从 token 读取前缀，无需额外参数。旧版不支持命名空间路径的 token 仍不支持。MCP 地址为 `<公开域名><url-prefix>/mcp`。

</details>

<details>
<summary>会话行为与平台限制</summary>

- `create_client` 成功后，Agent 先展示 `display_text`，再使用返回的 `client_token` 以合理间隔轮询 `list_clients`（例如每 2 秒一次）。没有设备在线时返回 `{"clients":[]}`；目标上线后才执行命令或启动会话。等待工具已移除。
- `session_read` 通过轮询获取输出，`wait_ms` 最多等待 20000 毫秒；`done: true` 时返回 `exit_code`。每个会话保留最近 1 MiB 输出，读取落后时返回 `truncated: true`。每个 token 最多保留 6 条会话记录，多设备模式下所有设备共用此额度；新建会话时若已满额，优先淘汰最早结束的记录，6 个会话都在运行时拒绝新建。已结束超过 30 分钟的记录也会由每分钟的定时任务或新建会话时清理；不同 token 的额度互不影响。
- macOS/Linux 会话使用 PTY。Windows 使用管道，支持简单提示交互，不支持要求真实终端的程序。暂不支持远程图形界面。
- 全屏终端程序输出的控制序列可能无法被 Agent 正确理解。CLI 会过滤本地日志中的控制序列，它是日志查看器。输入可能被回显，避免通过 Agent 输入敏感信息。
- 命令使用直接启动 Client 的受支持 shell（PowerShell、cmd、bash、zsh、fish 等）。无法识别时，macOS/Linux 回退到 `/bin/sh`，Windows 回退到 `ComSpec`（通常是 `cmd.exe`）。每条命令启动新的 shell，不共享启动 shell 的会话变量和函数。
- Windows 根据首段非 ASCII 内容识别 UTF-8 或系统代码页；混合编码仍可能乱码。
- 临时断线后自动重连，设备 ID 会变化，需要通过 `list_clients` 重新发现设备；Server 重启后需获取新 token。
- GUI 统计已完成命令数和平均耗时，包含失败或取消的命令。清空日志只影响显示，自动重连保留统计，手动开始新连接时重置统计；连接过程中可取消。

</details>

<details>
<summary>安全模型</summary>

Server 可见命令与输出，Client 也会在本地显示，两者都可能包含敏感信息。关闭 Client 会尝试停止运行中的命令，但无法撤销已完成操作，也不能保证停止脱离的子进程。

默认 HTTP 和 WebSocket 不加密，流量监听者可读取凭证、命令和输出。不可信网络使用 HTTPS，并保管好 token 和管理密钥。

认证按客户端 IP 限流：1 分钟内失败 10 次后封禁该 IP 5 分钟，期间该 IP 的正确凭证也会被拒绝，其他 IP 不受影响。默认使用直连对端 IP，并忽略转发头；代理部署时通过 `--client-ip-header <头名>` 指定 IP 来源。请求头模式下，缺失、多个值或非法 IP 返回 HTTP 400，不回退到其他来源；必须限制 Server 仅供可信代理访问，并由代理覆盖该头。共享公网 IP 的用户相互影响，分布式来源仍可继续尝试认证。

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

发布版 CLI 使用 `-gcflags=all=-l` 关闭函数内联，以减小程序和下载包的体积；CI 使用相同的编译设置测试 CLI。

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

## 发布

推送 `v*` tag 仍会全量构建并发布 GitHub 预发布版。按需构建时，先提交并推送新版本号及代码，再到 **Actions → Release → Run workflow** 选择分支和构建范围。不要提前推送新 tag：手动流程根据 `internal/version.Current` 确定版本，检查通过后为实际构建的提交创建 tag。重建 GUI 时，`cmd/agenterm-gui/FyneApp.toml` 的版本号需保持一致。

| `scope` | 重新构建 | 从基准 Release 复用 |
| --- | --- | --- |
| `all`（默认） | CLI、GUI、Server | 无 |
| `server` | Server | CLI、GUI |
| `client` | CLI、GUI、Server | 无；客户端公共代码影响两种客户端，Server 内嵌 CLI |
| `cli` | CLI、Server | GUI |
| `gui` | GUI | CLI、Server |

例如，只重建 Server：

```sh
gh workflow run release.yml --ref main -f scope=server
# 也可以指定从哪个已发布版本复用产物：
gh workflow run release.yml --ref main -f scope=server -f base_tag=v0.2.26
```

两条命令二选一，同一版本只触发一次。`base_tag` 留空时选择发布时间最近、且 tag 属于当前提交祖先的 Release，也包含 GitHub 预发布版。没有可用基准时全量构建；主动指定的基准不存在或不属于当前历史时则报错。不会覆盖已有 Release。

当前由发布者手动选择范围，不会自动分析代码变更。需包含所有受影响的程序：仅安装脚本或服务端变化可选 `server`；客户端公共代码变化选 `client`；公共依赖、协议、翻译或打包逻辑变化通常选 `all`。升级 Go 后的首次发布必须使用 `all`，确保所有程序都由新工具链重新构建。复用产物不会跳过相关测试，Windows 安装脚本及客户端测试仍会执行。

复用程序保留原始二进制版本号。流程按实际连接兼容规则检查这些版本，只要有一个与新版本不兼容，就改为全量构建。复用下载文件时，对照 GitHub 附件元数据中的 SHA256 校验，复用文件保持字节一致，新构建的 Server 内嵌选定的 CLI，发布前检查各平台产物完整。文件缺失或损坏时停止发布。`release-manifest.json` 记录每种程序的原始来源 tag 和版本，连续复用也不会丢失来源；Release 说明列出构建／复用情况并附上自动生成的变更记录。Release 附件只包含程序文件和 `release-manifest.json`，SHA256 由 GitHub 提供。安装脚本及供安装程序使用的 `SHA256SUMS` 仍由 Server 内嵌或生成，并通过 Server 下载接口提供。

工作流需先进入默认分支，才会显示手动运行入口。参见 [GitHub 手动运行工作流说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。

## 测试

```sh
go test ./...
```

## 协议

[Apache-2.0](LICENSE)

## 语言

桌面客户端默认跟随系统语言。可在应用菜单的 **Language / 语言** 中选择英文、简体中文或跟随系统；重启应用后生效。终端客户端和服务端跟随终端语言环境。不支持的语言统一回退为英文。可设置 `AGENTERM_LANG=en` 或 `AGENTERM_LANG=zh-CN`，覆盖终端进程及其安装脚本的语言。命令输出保持原样。

所有应用文案统一维护在 `internal/i18n/messages.json`，使用稳定的 key，提供 `en` 和 `zh-CN` 两种翻译。Go 代码引用 key；安装脚本由服务端从同一资源注入翻译。新增文案时请在此补齐两种语言，并保持格式参数一致。
