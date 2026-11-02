# 使用指南

[English](usage.md) · [返回首页](../README.zh-CN.md) · [自托管](deployment.zh-CN.md)

## 接入 Agent

将支持 Streamable HTTP 的 Agent 接入：

```text
https://agenterm.net/mcp
```

以 Codex 为例，在 `~/.codex/config.toml` 中添加：

```toml
[mcp_servers.agenterm]
url = "https://agenterm.net/mcp"
```

重新加载 MCP 配置或重启 Agent 后：

1. 告诉 Agent 你要完成的任务，并让它为目标电脑创建连接邀请。
2. 将 Agent 给出的一键连接命令私下发送给目标电脑的使用者。
3. 对方运行适合其系统的命令，阅读远程控制提示并确认授权。
4. 保持 Agent 对话进行，等待设备上线。连接成功后，Agent 即可执行已授权的任务，目标电脑能实时查看过程。
5. 任务结束后，关闭客户端或主动断开连接。

邀请默认 5 分钟有效、只能使用一次。调用 `create_client` 时可传入可选的正整数参数 `timeout_seconds` 自定义有效期，单位秒；例如 `{"timeout_seconds": 60}` 会创建 60 秒有效的邀请。过期后不能兑换邀请，已连接的设备不会因此断开。需要连接多台电脑时，让 Agent 创建多设备邀请，并在操作前明确选择目标设备。

Token 成功连接过后，只要仍有设备在线就保持有效。最后一台设备断开后，会保留 30 分钟供重连；期间重连成功会清除离线过期倒计时，否则 Token 到期后需要重新生成邀请。这段离线保留时间与邀请有效期分别计算。

托管服务目前无需管理认证，按邀请创建者的公网 IP 限制最多 4 台在线设备；同一网络下的使用者共享额度。请私密保管邀请和连接 token。需要自行控制管理访问时，使用自托管方式。

## MCP 工具与返回结果

当前源码构建会为全部七个工具提供明确的返回 Schema 和行为标注。请以实际连接 Server 的 `tools/list` 响应为准，既有部署或较旧发布的二进制可能仍提供旧版 Schema。

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `client_token`、`invitation`、`expires_at`、平台命令、`display_text`。 |
| `list_clients` | 查看设备或等待连接。 | `clients`；等待时还返回 `status`。 |
| `execute_command` | 运行短时非交互命令并等待结果。 | `stdout`、`stderr`、`exit_code`、`timed_out`、`canceled`、`truncated`，以及可选 `error`。 |
| `session_start` | 启动交互或长时间运行的进程。 | `session_id`、初始 `cursor=0`。 |
| `session_read` | 分页读取输出并检查会话是否完成。 | `output`、`next_cursor`、`has_more`、`truncated`、`done`；终态还包含退出、超时、取消及错误字段。 |
| `session_control` | 用 `action` 选择输入、中断、EOF 或停止。 | `action`；输入返回 `accepted`，中断/EOF 返回 `delivered`，停止返回确认状态及终态字段。 |
| `session_list` | 找回所选 token 的保留会话，可按 `client_id` 筛选，无需设备在线。 | `sessions`：会话、设备、原始启动命令、时间、状态及保留输出游标范围。 |

`create_client` 的 MCP 文本内容直接返回固定的英文纯文本连接模板，与 `structuredContent.display_text` 一致，不受服务器语言设置影响；凭据、有效期和平台命令仍保留在 `structuredContent` 中，供程序读取。模板依次包含 Windows 命令、macOS / Linux 命令、连接令牌、使用方法和邀请有效期；多设备模式会追加复用提示。

通过 `create_client` 生成连接说明后，agent 必须先根据与用户交流的语言完整展示 `display_text`，仅翻译标题和说明文字；使用英语交流时原样展示。保留所有段落、顺序、换行和纯文本格式，命令、URL、连接令牌、有效期数值及截止时间保持原样，不得省略内容或添加 Markdown 格式。然后使用返回的 `client_token` 调用 `list_clients`，设置 `wait_ms: 60000`，以 60 秒为超时时间检查是否连接成功。任一设备在线时立即返回；60 秒内仍未连接则向用户报告超时。这些要求直接写在 MCP 工具说明中。

`session_control` 必须提供 `session_id` 和 `action`。仅 `action=input` 可带且必须带 `input`；仅 `action=stop` 可附带 `wait_ms`（默认 1000）。其他 action 不得带这些参数，混用会被拒绝。每个成功结果均带 `action`，发送输入、控制送达和停止确认使用不同的返回 Schema。由于输入和中断可能重复产生副作用，整个工具保守标注为非幂等；重复 stop 仍按下文语义处理。管理访问可见七个工具；设备范围访问可见六个，不含 `create_client`。

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

`list_clients` 为每台设备返回 `status`、`client_id`、`connected_at` 和 `client_info` 对象。设备的 `status` 显示 `Connected`，有测量值时附带最近一次服务端与客户端之间的心跳往返延迟，例如 `Connected · 203ms`。`client_info` 包含 `hostname`、`platform`、`arch`（客户端进程架构）、`shell`（命令和会话实际使用的解释器路径）、`default_cwd`（未指定 `cwd` 时继承的客户端工作目录）和 `client_version`（握手中上报的客户端应用版本）。元数据字段为空表示未知或客户端未上报。Server 和 client 均需更新才能提供新增元数据。

每次 `execute_command` 都启动独立 shell，请显式设置 `cwd` 和 `env`，它们不会跨命令保留。默认超时为 60 秒，`timeout_ms=0` 表示不限时，stdout/stderr 共用 1 MiB 上限。交互提示、增量输出或长任务应使用会话工具，保留 `session_id` 和按字节计数的 `next_cursor`。会话缓冲区只保留最近 1 MiB，出现 `truncated` 时应检查是否丢失较旧输出。

需要保留目录、变量、函数或虚拟环境状态时，用 `session_start` 显式启动常驻 shell（例如 Linux/macOS 上已安装的 `bash -i`），再通过 `session_control(action="input")` 向同一个 `session_id` 发送命令。每次 `session_start` 创建独立进程；一次性命令退出后不会留下常驻 shell。客户端断线会结束会话，重连不会恢复 shell 状态。

`session_read.max_bytes` 默认 16384，范围 4–65536 字节；输出不会在 UTF-8 字符中间切断。每次用返回的 `next_cursor` 继续读取。`has_more=true` 表示当前仍有保留输出；即使 `done=true`，也应读到 `has_more=false`。`done` 和 `exit_code` 属于整个会话进程，不能用于判断常驻 shell 中单条命令是否完成。分页本身不会触发 `truncated`。

`session_list` 按启动时间返回本 token 的会话，包含离线设备的保留记录。`output_start_cursor` 指向最早保留输出，`output_end_cursor` 指向当前输出末尾；使用后者读取会跳过已有输出。会话 `status` 为 `running`、`stopping` 或 `finished`，不推断 shell 是否空闲、当前目录或单条命令状态。结束记录保留最多 30 分钟，也可能在新建会话时被替换。

调用中断或 EOF 前，先检查 `list_clients.client_info.session_controls`（`interrupt` 和/或 `eof`）；空列表表示不可用，旧客户端可能需要更新。这两个操作最多等待 5 秒取得客户端确认；`delivered=true` 不保证程序已处理，应继续读取输出核实。超时或取消后操作可能已经执行，请勿盲目重试。

- `session_control(action="interrupt")` 在 Linux/macOS 向终端当前前台进程组发送 SIGINT，前台命令退出后交互 shell 可能继续可用；程序也可能忽略信号或退出。它不会像 `session_control(action="stop")` 一样强制取消整个会话。Windows 管道模式不支持中断。
- `session_control(action="eof")` 排在先前输入之后执行。在 Linux/macOS 的规范终端模式下发送配置的 VEOF 字符；若有未提交的输入，会先交付这些输入，可能需要再次 EOF，且不会永久关闭输入。原始模式或禁用 VEOF 时返回错误。在 Windows 管道模式下永久关闭标准输入，输出仍可读取，后续写入失败，重复 EOF 无额外影响。在空闲 shell 提示符处发送 EOF 可能退出 shell。

`session_control(action="stop")` 最多等待 `wait_ms` 毫秒取得终态，默认 1000，范围 0–20000；设为 0 时立即返回。按以下方式判断结果：

| 状态 | 含义及下一步 |
| --- | --- |
| `stopping` | 请求已发出或正在等待处理，尚未确认停止，继续 `session_read` 直到 `done=true`。 |
| `stopped` | 目标已返回 `canceled=true` 的终态，可继续读取剩余会话输出。 |
| `finished` | 会话因其他原因结束，应检查 `exit_code`、`timed_out`、`canceled` 和 `error`；断线不代表远程取消已确认。 |

不能仅凭 `accepted=true` 宣布进程已停止。取消待确认时重复调用不会再次发送停止请求；调用前已结束的会话会返回 `already_done=true`、`accepted=false`，无需报错。未知或无权限的会话仍会报错。停止不能撤销已完成操作，也不会撤销设备凭据或停止其他会话。命令和输入写入不要盲目重试，否则可能重复产生副作用。

## 客户端操作

**终端客户端（CLI）**默认进入终端界面。多行命令和输出会折叠，点击展开或收起。

| 操作 | 方法 |
| --- | --- |
| 浏览日志 | 方向键或鼠标滚轮。 |
| 复制内容 | 拖选文字，松开鼠标自动复制；保留选区时按 Ctrl+C 可再次复制。 |
| 跨屏选择 | 按住鼠标拖到日志区域的上、下边缘。 |
| 清除选区 | 按 Esc 或点击日志外。 |
| 清空日志 | 按 `c`。 |
| 断开并退出 | 按 `q`，或在没有选区时按 Ctrl+C。 |

需要纯文本输出时使用 `./agenterm-client-cli -plain`；重定向输入或输出时自动启用。通过 SSH 使用复制功能时，需要终端支持 OSC 52 剪贴板访问。

**桌面客户端（GUI）**支持粘贴 token、连接和断开、查看实时命令与输出，跟随系统明暗主题，并显示连接延迟和命令统计。

两个客户端均支持 `-token <token>` 或 `-invite <邀请>`（也支持 `-参数=值` 写法），通过 `-help` 查看可用参数。两种连接参数不能同时使用。邀请连接需要在终端或 GUI 弹窗中授权。`-plain` 和安装脚本使用的 `-consent-confirmed` 仅供 CLI 使用；GUI 会拒绝这两个参数，并始终对邀请连接显示授权弹窗。

客户端在内存中保留最近最多 10 MiB 日志，不保存连接 token。清空日志只影响本地显示。

## 常见问题

### 连接失败或邀请过期

- 确认 Agent 和目标电脑都能访问服务器地址。`127.0.0.1` 仅适用于同一台电脑。
- 邀请过期或已使用时，让 Agent 重新生成邀请。
- Runtime 重启后，原有邀请和 token 失效，需要重新创建连接；单独重启 Server 会保留连接与会话。
- 遇到版本不兼容提示时，从同一个 Release 更新 Server 和客户端。
- 提示已有连接时，先关闭该系统账户下连接同一服务器的另一个 CLI 或 GUI。

### 网络断开后怎么办

客户端会自动尝试重连。断线时会尝试取消正在执行的命令，恢复连接后不会自动重放；让 Agent 重新确认设备和任务状态。对话中的等待需要 Agent 持续运行，已经结束的对话不会在设备上线后自动唤醒。

### 支持哪些程序

支持 macOS、Linux 和 Windows 上的命令行任务。Windows 的交互支持适用于简单输入提示，要求真实终端的全屏程序可能无法正常使用。每条普通命令独立执行；需要连续交互时，让 Agent 使用交互式会话。

### 权限与隐私

连接 token 授予目标电脑当前用户的终端操作权限。仅在取得对方授权后使用，完成后断开连接。Agent、Server 和目标客户端都能看到命令与输出，请避免通过会话传递密码等敏感信息。关闭客户端无法撤销已经完成的操作。

默认 HTTP 连接不加密。跨不可信网络使用时，请通过 HTTPS 部署，并妥善保管管理密钥、邀请和 token。

### 缓存与移除

一键连接下载的 CLI 缓存保存在：

| 系统 | 缓存目录 |
| --- | --- |
| macOS | `~/Library/Caches/Agenterm` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/agenterm` |
| Windows | `%LOCALAPPDATA%\Agenterm\cache` |

关闭客户端后可以手动删除缓存。一键连接不会安装开机自启服务，后续使用仍需授权。

### 语言设置

桌面客户端默认跟随系统语言，可在应用菜单的 **Language / 语言** 中选择英文、简体中文或跟随系统，重启后生效。CLI 和 Server 跟随终端语言环境，可通过 `AGENTERM_LANG=en` 或 `AGENTERM_LANG=zh-CN` 指定语言。命令输出保持原样。
