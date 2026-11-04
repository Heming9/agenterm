# MCP 工具技术参考

当前源码构建会为全部七个工具提供明确的返回 Schema 和行为标注。请以实际连接 Server 的 `tools/list` 响应为准，既有部署或较旧发布的二进制可能仍提供旧版 Schema。

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `client_token`、`invitation`、`expires_at`、平台命令、`display_text`。 |
| `list_clients` | 查看在线设备或等待连接。 | `clients`；空数组表示返回时无在线设备。 |
| `p2p_transfer_start` | 让远端自动下载并初始化 p2p-cli，注册协商后返回 Agent 端脚本。 | `connection_id`、双方客户端 ID、`state`、`shell_script`、`powershell_script`。 |
| `execute_command` | 通过 setup 创建、session_id 复用；匿名任务快速完成关闭，等待到期保留。 | `session_id`、`command_id`、`status`、`output`、`next_cursor`、`has_more`；按需返回 `exit_code`、`error`、`truncated`。 |
| `session_read` | 分页读取输出，与 execute_command 使用同一返回格式。 | 六个常规字段；命令结束时带 `exit_code`，有错误或丢失输出时带对应字段。 |
| `session_control` | 用 action 选择输入、中断、EOF 或停止。 | 输入/中断/EOF 返回 `ok:true`；stop 返回 `status` 和可选 `exit_code/error`。 |
| `session_list` | 按创建顺序找回本 token 的会话，可按 client_id 筛选。 | `sessions`：`session_id`、`client_id`、`command`、`command_id`、`status`；可选 `exit_code/error`。 |


## 常用调用

创建邀请后，将连接命令私下交给目标用户；设备连接后可执行命令。后续命令通过 execute_command 指定 session_id 复用终端；当前命令等待输入时，使用 session_control：

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

工具和字段以已连接服务的 `tools/list` 为准。

匿名终端中快速执行，完成后自动关闭：

```json
{"command":"pwd"}
```

初始化独立终端并启动长任务：

```json
{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}
```

复用返回的终端并保留目录和环境：

```json
{"session_id":"…","command":"git status"}
```


## 详细说明

<details>
<summary>邀请创建与展示</summary>

`create_client` 的 MCP 文本内容直接返回固定的英文纯文本连接模板，与 `structuredContent.display_text` 一致，不受服务器语言设置影响；凭据、有效期和平台命令仍保留在 `structuredContent` 中，供程序读取。模板依次包含 Windows 命令、macOS / Linux 命令、连接令牌、使用方法和邀请有效期；多设备模式会追加复用提示。

通过 `create_client` 生成连接说明后，agent 必须先根据与用户交流的语言完整展示 `display_text`，仅翻译标题和说明文字；使用英语交流时原样展示。保留所有段落、顺序、换行和纯文本格式，命令、URL、连接令牌、有效期数值及截止时间保持原样，不得省略内容或添加 Markdown 格式。然后使用返回的 `client_token` 调用 `list_clients`，设置 `wait_ms: 300000`，以 300 秒为超时时间检查是否连接成功。任一设备在线时立即返回；300 秒内仍未连接则向用户报告超时。这些要求直接写在 MCP 工具说明中。

</details>

<details>
<summary>设备信息</summary>

`list_clients` 只返回在线设备。每条包含 client_id、connected_at 和 client_info，不再返回顶层或设备条目的 status；是否在线由 clients 数组直接判断。client_info 的主机名、平台、架构、shell、默认目录、客户端版本和能力信息保持不变。心跳和 RTT 仍由连接内部及客户端界面使用，不拼接为 MCP 状态字符串。

</details>

<details>
<summary>职责与状态来源</summary>

| 职责 | 所属端 |
| --- | --- |
| 终端 ID、创建与复用、忙碌和输入校验 | 客户端共用执行核心，CLI/GUI 行为一致 |
| 10 个存活终端上限、实际进程、等待到期保留与关闭 | 客户端 |
| 鉴权、设备路由、请求关联、结果等待和输出缓存 | 服务端 |
| 已结束记录的缓存容量与保留期限 | 服务端，仅清理缓存，不关闭终端 |

客户端先回报实际终端 ID、是否新建和保留策略，服务端再绑定输出缓存。服务端不会依据缓存状态选择终端或决定其生命周期。即使 MCP 等待已取消，后续回执仍可建立缓存，供 session_list 找回。匿名终端的完成关闭、等待到期保留均由客户端裁决；服务端清理记录不会关闭终端。

</details>

<details>
<summary>命令与交互式会话</summary>

`execute_command` 的顶级 `session_id` 只复用已有终端，未知或已关闭 ID 报错。传 `setup` 始终新建终端，其中可包含 `cwd`、`env` 和 `session_id`；指定 ID 已存在时会报错，不覆盖或复用。setup 与顶级 session_id 不能同时使用。setup.session_id 指定时创建持久终端；省略时生成随机 ID，仍走匿名终端流程。两者均省略也新建匿名终端：快速完成自动关闭，等待先到期则保留同一 shell。同一 token 内 ID 唯一，多设备创建需 client_id，已有 ID 可定位设备。

目录、变量、函数和虚拟环境状态在同一 shell 中保留。`cwd` 和 `env` 只存在于 setup 中，用于初始化新终端；复用时不传 setup，通过 cd、export 或目标 shell 对应命令修改状态。每个终端同一时间执行一条命令，忙碌时返回 session_busy，不另开终端、不排队。

`wait_ms` 默认 10000（10 秒），范围 0–20000，从客户端接受开始计时，包括新 shell 的启动时间。0 立即保留匿名终端，仍需等待接受回执。等待到期不会终止命令。执行中返回 status=running，完成且保留 shell 时返回 idle，会话不可用后返回 closed。具名和等待到期保留的终端需 session_control(action=stop) 主动关闭，不按空闲时间回收。MCP 等待取消不会取消已接受任务。

execute_command 与 session_read 统一返回六个常规字段：session_id、command_id、status、output、next_cursor、has_more。status 为 running（终端忙碌，包括停止待确认）、idle（命令结束，终端可复用）、closed（会话不可用；断线时远端终止未确认）。快速匿名命令结束后为 closed，具名或保留终端的命令完成后为 idle。命令完成时额外返回 exit_code，包括成功的 0；仅有错误时返回 error，仅旧输出被丢弃时返回 truncated=true。不再返回 session_created、command_done、done、session_alive、persistent、promoted、timed_out 或 canceled。

Linux/macOS 使用上报的 shell 和 PTY；Windows 使用 PowerShell 和管道，支持基本输入交互，不模拟完整终端。为保持命令边界可靠，持续终端不加载用户 shell 启动配置；继承客户端环境，并保留提示符钩子、PS1 等供集成使用，请勿覆盖它们或修改终端回显。需要的环境可以在命令中显式初始化。服务端和客户端均需更新，旧客户端会收到升级提示。

每个客户端进程最多 10 个存活终端，启动、运行、空闲和关闭中的 shell 在实际退出前都占名额。CLI/GUI 共用客户端执行层，同一进程内多个连接共享上限；同一 token 下不同进程各自计数。满额时新建匿名终端、创建尚不存在的具名终端均直接报错；明确指定已有 session_id 仍可复用，不自动选择其他终端。已关闭输出记录不占名额。服务端仅清理输出缓存，不检查终端配额或触发空闲关闭。客户端断线会终止该连接的终端，重连不恢复 shell 状态；服务端前端重启不影响 runtime 中已有连接和终端；runtime 重启则丢失令牌、邀请和会话。

</details>

<details>
<summary>读取输出与查找会话</summary>

`session_read.max_bytes` 默认 16384，范围 4–65536 字节，输出不会拆开 UTF-8 字符。每次使用 next_cursor 继续读取；即使 status 为 idle/closed，也应读到 has_more=false。没有输出或分页读完不代表命令结束；请检查 status。truncated 仅在旧输出丢失时出现，分页本身不会触发它。

`session_list` 按创建时间升序返回本 token 的保留会话，包括已结束记录，可按 client_id 筛选。每条仅返回 session_id、client_id、command、command_id、status，完成后按需附带 exit_code/error。状态与执行/读取一致：running、idle、closed。时间、输出游标范围及重复布尔状态不再对外返回；已结束记录仍保留最多 30 分钟或在新记录到来时替换，用于找回丢失回执的任务。

`session_read` 可附带 `command_id` 固定读取的命令；若终端已执行下一条命令则报错，避免把新命令的状态当成旧命令结果。不带此字段仍可按字节游标读取整个会话的保留输出。缓冲区保留最近 1 MiB，较旧输出被丢弃时设置 `truncated=true`，单纯分页不会设置它。命令完成后仍应读到 `has_more=false`。

</details>

<details>
<summary>会话控制：输入、中断、EOF 和停止</summary>

`session_control` 必须提供 session_id 和 action。input 仅向当前命令发送输入，空闲终端拒绝；新命令使用 execute_command。仅 input 接受且必须提供 input，仅 stop 接受可选 wait_ms（默认 1000）。input、interrupt、eof 成功统一返回 `{"ok":true}`，表示客户端已确认请求，不保证程序已消费输入或处理中断。stop 返回 status 和可选 exit_code/error。不回显 action。输入和信号可能重复产生副作用，超时后勿盲目重试。

调用中断或 EOF 前，先检查 `list_clients.client_info.session_controls`（`interrupt` 和/或 `eof`）；空列表表示不可用，旧客户端可能需要更新。这两个操作最多等待 5 秒取得客户端确认；`ok=true` 不保证程序已处理，应继续读取输出核实。超时或取消后操作可能已经执行，请勿盲目重试。

- `session_control(action="interrupt")` 在 Linux/macOS 向终端当前前台进程组发送 SIGINT，前台命令退出后交互 shell 可能继续可用；程序也可能忽略信号或退出。它不会像 `session_control(action="stop")` 一样强制取消整个会话。Windows 管道模式不支持中断。
- `session_control(action="eof")` 排在先前输入之后执行。在 Linux/macOS 的规范终端模式下发送配置的 VEOF 字符；若有未提交的输入，会先交付这些输入，可能需要再次 EOF，且不会永久关闭输入。原始模式或禁用 VEOF 时返回错误。在 Windows 管道模式下永久关闭标准输入，输出仍可读取，后续写入失败，重复 EOF 无额外影响。在空闲 shell 提示符处发送 EOF 可能退出 shell。

`session_control(action="stop")` 最多等待 `wait_ms` 毫秒取得终态，默认 1000，范围 0–20000；设为 0 时立即返回。按以下方式判断结果：

| 状态 | 含义及下一步 |
| --- | --- |
| `running` | 停止请求已发送或待确认，继续读取状态；不代表远端进程已经退出。 |
| `closed` | 会话已不可用，可继续读取保留输出；若有断线 error，远端进程终止尚未确认。 |


重复 stop 在待确认时不再次发送取消请求，已结束时返回相同终态。未知或无权限会话仍报错。closed 表示会话不可用，不应忽略 error 将断线误报为停止成功。stop 不撤销已完成操作，不撤销设备凭据，也不停止其他会话。

</details>
