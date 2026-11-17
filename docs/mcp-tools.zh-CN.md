# MCP 工具技术参考

[English](mcp-tools.md) · [使用指南](usage.zh-CN.md)

以下是当前源码的工具契约。不同部署或较旧的发行版可能使用较早的 Schema，例如以 `client_token` 返回设备令牌。以实际连接服务端的 `tools/list` 为准。

## 访问方式

| 方式 | HTTP 凭据 | 工具调用 |
| --- | --- | --- |
| 管理访问 | `Authorization: Bearer <管理密钥>`。服务端启用 `-no-password` 时可省略请求头。 | 共 8 个工具，含 `create_client`。其余工具均需要调用参数 `control_token`。 |
| 限定访问 | `Authorization: Bearer <control_token>` | 共 7 个工具，作用于该凭据下的设备。调用参数省略 `control_token`。 |

`create_client` 返回的 `device_token` 供目标客户端连接。`control_token` 用于执行和管理，应由 Agent 私下保管。管理密钥用于接入自托管 MCP，配置示例见[使用指南](usage.zh-CN.md#接入-agent)。

## 工具列表

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `device_token`、`control_token`、`invitation`、`expires_at`、平台命令、`display_text`。 |
| `list_client` | 查看在线设备或等待连接。 | `clients`；空数组表示返回时无在线设备。 |
| `close_client` | 关闭指定设备或当前 token 下的全部在线设备。 | 已关闭的 `client_ids`。 |
| `p2p_transfer_start` | 让远端自动下载并初始化 p2p-cli，注册协商后返回 Agent 端脚本。 | `connection_id`、双方客户端 ID、`state`、`shell_script`、`powershell_script`。 |
| `execute_command` | 通过 setup 创建、session_id 复用；匿名任务快速完成关闭，等待到期保留。 | `command_id`、`status`、`output`、`next_cursor`、`has_more`；按需返回 `session_id`、`exit_code`、`error`、`truncated`。 |
| `session_read` | 分页读取输出，与 execute_command 使用同一返回格式。 | `session_id`、`command_id`、`status`、`output`、`next_cursor`、`has_more`，以及按需返回的 `exit_code`、`error`、`truncated`。 |
| `session_control` | 用 action 选择输入、中断、EOF 或停止。 | 输入/中断/EOF 返回 `ok:true`；stop 返回 `status` 和可选 `exit_code/error`。 |
| `session_list` | 查找保留的会话。结果按设备分组，同一设备内按会话创建顺序排列。 | `sessions`：`session_id`、`client_id`、`command`、`command_id`、`status`；可选 `exit_code/error`。 |

## 常用调用

创建邀请并私下分享连接信息。设备上线后，用 `execute_command` 运行命令。下面是管理访问下的匿名命令，完成后自动关闭终端。

```json
{"control_token":"CONTROL_TOKEN","command":"pwd"}
```

其余示例使用限定访问。多设备令牌调用 `execute_command`、`session_read` 和 `session_control` 时还需指定 `client_id`。

用 `execute_command` 创建具名终端，完成后可用顶级 `session_id` 复用目录和环境。

```json
{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}
```

```json
{"session_id":"build","command":"git status"}
```

当前命令需要输入时，调用 `session_control`。

```json
{"session_id":"SESSION_ID","action":"input","input":"yes\n"}
```

## 详细说明

<details>
<summary>邀请创建与展示</summary>

`create_client` 的 MCP 文本内容直接返回固定的英文连接模板，其中每条命令自带 Markdown 代码块，与 `structuredContent.display_text` 一致，不受服务器语言设置影响；凭据、有效期和平台命令仍保留在 `structuredContent` 中，供程序读取。模板依次包含 Windows 命令、macOS / Linux 命令、连接令牌、启用时的短码和浏览器邀请文案、使用方法和邀请有效期；多设备模式会追加复用提示。

单设备邀请启用短码时，`short_code_url` 是可在浏览器查看连接信息的私密链接（例如 `example.com/c/abcdef`）。设备在线期间短链接和短码不可使用，断线后仍在有效期内时可重新使用。它与短码共用有效期，并在第 10 次成功兑换后失效；浏览器通过 GET 查看页面，客户端通过 POST 兑换。安装脚本使用独立的设备令牌密钥路径。

1. 按原顺序以用户的语言展示 `display_text` 的全部段落。保留命令、URL、令牌和短码的原文，每条命令、连接令牌和浏览器邀请文案各放一个 Markdown 代码块。
2. 用易读单位显示有效期。已知用户时区时，将 `expires_at`、`token_initial_expires_at` 和存在时的 `short_code_expires_at` 换算到该时区并标注。时区未知时使用 UTC。
3. 私下保管 `control_token`（格式为 `device_token:密钥`）。展示邀请后，用它调用 `list_client`，以 `wait_ms: 30000` 分轮等待，共用 300 秒的实际时间预算。任一设备上线即可继续。收到空列表时继续等待，直到预算耗尽，再用 `wait_ms: 0` 最后检查一次，仍无在线设备才报告超时。
4. 遇到临时网络错误时，用 `wait_ms: 0` 复查一次。复查仍失败时报告连接状态未知。重试沿用原预算。用户取消或凭据无效、过期、撤销时停止等待，单独说明凭据错误。

前端兼容最高 `300000` 的旧等待参数，每轮实际等待上限仍为 30000 毫秒。

</details>

<details>
<summary>设备信息</summary>

`list_client` 只返回在线设备。每条包含 client_id、connected_at 和 client_info，不再返回顶层或设备条目的 status；是否在线由 clients 数组直接判断。client_info 的主机名、平台、架构、shell、默认目录、客户端版本和能力信息保持不变。心跳和 RTT 仍由连接内部及客户端界面使用，不拼接为 MCP 状态字符串。

`close_client` 接受非空、无重复的 `client_ids` 数组；不传该参数时关闭当前 token 下所有在线设备并撤销该 token 和其邀请，即使没有在线设备也会撤销。所有 ID 均须属于该 token，校验失败时不关闭任何设备。返回的 `client_ids` 列出已关闭的连接，无设备在线时返回空数组。关闭会结束设备的会话和活动传输；仅关闭指定设备时保留 token 和邀请。已关闭客户端不会自动重连。

</details>

终端生命周期、结果保留及 WebSocket 转发见[终端客户端 API](client-api.md)。离线设备恢复连接后方可查询会话。

<details>
<summary>命令与交互式会话</summary>

`execute_command` 的顶级 `session_id` 只复用已有终端，未知或已关闭 ID 报错。传 `setup` 始终新建终端，其中可包含 `cwd`、`env` 和 `session_id`；指定 ID 已存在时会报错，不覆盖或复用。setup 与顶级 session_id 不能同时使用。setup.session_id 指定时创建持久终端；省略时生成随机 ID，仍走匿名终端流程。两者均省略也新建匿名终端：快速完成自动关闭，等待先到期则保留同一 shell。会话 ID 仅在所属客户端内唯一。多设备 token 调用 execute_command、session_read、session_control 时始终需要 client_id，包括复用已有会话；不同设备可以使用相同 session_id。

目录、变量、函数和虚拟环境状态在同一 shell 中保留。`cwd` 和 `env` 只存在于 setup 中，用于初始化新终端；复用时不传 setup，通过 cd、export 或目标 shell 对应命令修改状态。每个终端同一时间执行一条命令，忙碌时返回 session_busy，不另开终端、不排队。

普通命令保留默认 10 秒等待，完成会提前返回；相关命令复用 idle 会话。等待新输出优先用 `session_read(wait_ms=10000–20000)`。读完输出后 stop 本任务已不再需要的会话。满额时用 `session_list(status="idle")` 查找可复用终端，或用 `status="live"` 检查清理对象；关闭后确认 `closed` 再创建新终端。

`wait_ms` 默认 10000（10 秒），范围 0–20000，从客户端接受开始计时，包括新 shell 的启动时间。0 立即保留匿名终端，仍需等待接受回执。等待到期不会终止命令。执行中返回 status=running，完成且保留 shell 时返回 idle，会话不可用后返回 closed。具名和等待到期保留的终端需 session_control(action=stop) 主动关闭，不按空闲时间回收。MCP 等待取消不会取消已接受任务。

execute_command 返回 command_id、status、output、next_cursor、has_more。具名或已保留终端返回 session_id；匿名终端仅在等待到期、输出超出单次响应上限或保留历史已被截断时返回 session_id。执行结果最多返回 16 KiB UTF-8 输出；has_more=true 表示可通过 session_read 继续读取剩余输出。已完成的匿名 shell 仍然关闭，返回的 ID 可用于读取客户端缓存。session_read 始终返回 session_id。status 为 running（终端忙碌，包括停止待确认）、idle（命令结束，终端可复用）、closed（客户端确认 shell 已结束）。快速匿名命令结束后为 closed，具名或保留终端的命令完成后为 idle。命令完成时额外返回 exit_code，包括成功的 0；仅有错误时返回 error，仅旧输出被丢弃时返回 truncated=true。

Linux/macOS 使用上报的 shell 和 PTY；Windows 使用 PowerShell 和管道，支持基本输入交互，不模拟完整终端。为保持命令边界可靠，持续终端不加载用户 shell 启动配置；继承客户端环境，并保留提示符钩子、PS1 等供集成使用，请勿覆盖它们或修改终端回显。需要的环境可以在命令中显式初始化。服务端和客户端均需更新，旧客户端会收到升级提示。

每个客户端进程最多 10 个存活终端，启动、运行、空闲和关闭中的 shell 在实际退出前都占名额。同一进程内多个连接共享上限；同一 token 下不同进程各自计数。满额时新建匿名终端、创建尚不存在的具名终端均直接报错；明确指定已有 session_id 仍可复用，不自动选择其他终端。已关闭输出记录不占名额。客户端独立管理输出缓存。服务端前端重启保留 runtime 中的连接；服务端 runtime 重启丢失服务端凭证和连接，不生成任何终端结果。

</details>

<details>
<summary>读取输出与查找会话</summary>

`session_read.max_bytes` 默认 16384，范围 4–65536 字节，输出不会拆开 UTF-8 字符。每次使用 next_cursor 继续读取；即使 status 为 idle/closed，也应读到 has_more=false。没有输出或分页读完不代表命令结束；请检查 status。truncated 仅在旧输出丢失时出现，分页本身不会触发它。

`session_list` 查询在线客户端，可按 `client_id` 和 `status` 筛选：`all`（默认）、`live`（running 或 idle）、`running`、`idle`、`closed`。复用查 `idle`，检查占用查 `live`。返回 `session_id`、`client_id`、`command`、`command_id`、`status` 及按需的 `exit_code`/`error`。命令预览最多 160 个 Unicode 字符，截断时包含末尾的 `... (truncated)`。已关闭历史不占终端名额，每客户端最多保留 30 分钟、10 条。

新命令 ID 为区分大小写的 8 位字母数字；旧客户端返回的较长 ID 仍原样使用。

`session_read` 可附带 `command_id` 固定读取的命令；若终端已执行下一条命令则报错，避免把新命令的状态当成旧命令结果。不带此字段仍可按字节游标读取整个会话的保留输出。客户端为保留终端缓存最近 1 MiB，请求的较旧输出被丢弃时设置 `truncated=true`。

</details>

<details>
<summary>会话控制：输入、中断、EOF 和停止</summary>

`session_control` 必须提供 session_id 和 action。input 仅向当前命令发送输入，空闲终端拒绝；新命令使用 execute_command。仅 input 接受且必须提供 input，仅 stop 接受可选 wait_ms（默认 1000）。input、interrupt、eof 成功统一返回 `{"ok":true}`，表示客户端已确认请求，不保证程序已消费输入或处理中断。stop 返回 status 和可选 exit_code/error。不回显 action。

调用中断或 EOF 前，先检查 `list_client.client_info.session_controls`（`interrupt` 和/或 `eof`）；空列表表示不可用，旧客户端可能需要更新。这两个操作等待 client API 响应，取消请求只结束调用方等待；`ok=true` 不保证程序已处理，应继续读取输出核实。输入或信号在超时或取消后可能已送达，请先查看输出再决定是否重试。

- `session_control(action="interrupt")` 在 Linux/macOS 向终端当前前台进程组发送 SIGINT，前台命令退出后交互 shell 可能继续可用；程序也可能忽略信号或退出。它不会像 `session_control(action="stop")` 一样强制取消整个会话。Windows 管道模式不支持中断。
- `session_control(action="eof")` 排在先前输入之后执行。在 Linux/macOS 的规范终端模式下发送配置的 VEOF 字符；若有未提交的输入，会先交付这些输入，可能需要再次 EOF，且不会永久关闭输入。原始模式或禁用 VEOF 时返回错误。在 Windows 管道模式下永久关闭标准输入，输出仍可读取，后续写入失败，重复 EOF 无额外影响。在空闲 shell 提示符处发送 EOF 可能退出 shell。

`session_control(action="stop")` 最多等待 `wait_ms` 毫秒取得终态，默认 1000，范围 0–20000；设为 0 时立即返回。按以下方式判断结果：

| 状态 | 含义及下一步 |
| --- | --- |
| `running` | 停止请求已发送或待确认，继续读取状态；不代表远端进程已经退出。 |
| `closed` | 客户端已确认 shell 结束，可继续读取保留输出。 |

重复 stop 安全，由客户端确认结束；已结束时返回其保存的 shell 结果。未知或无权限会话报错。传输断线返回错误，不会生成 status=closed 或退出码。stop 不撤销已完成操作，不撤销设备凭据，也不停止其他会话。

</details>
