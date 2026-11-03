# MCP 工具技术参考

当前源码构建会为全部七个工具提供明确的返回 Schema 和行为标注。请以实际连接 Server 的 `tools/list` 响应为准，既有部署或较旧发布的二进制可能仍提供旧版 Schema。

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `client_token`、`invitation`、`expires_at`、平台命令、`display_text`。 |
| `list_clients` | 查看设备或等待连接。 | `clients`；等待时还返回 `status`。 |
| `p2p_transfer_start` | 让远端自动下载并初始化 p2p-cli，注册协商后返回 Agent 端脚本。 | `connection_id`、双方客户端 ID、`state`、`shell_script`、`powershell_script`。 |
| `execute_command` | 匿名终端默认一次性，等待到期则保留；具名终端按名称创建或复用。 | `session_id`、`command_id`、`session_created`、`status`、`output`、`next_cursor`、`command_done`、`session_alive`，以及完成后的退出信息。 |
| `session_read` | 分页读取输出，分别检查命令完成与终端存活。 | `output`、`next_cursor`、`has_more`、`truncated`、`command_done`、`session_alive`、`done`；完成后提供退出信息。 |
| `session_control` | 用 `action` 选择输入、中断、EOF 或停止。 | `action`；输入返回 `accepted`，中断/EOF 返回 `delivered`，停止返回确认状态及终态字段。 |
| `session_list` | 找回所选 token 的保留会话，可按 `client_id` 筛选，无需设备在线。 | `sessions`：会话、设备、最新命令、时间、状态及保留输出游标范围。 |


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
{"command":"npm run build","cwd":"/project","session_id":"build","wait_ms":1000}
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

`list_clients` 为每台设备返回 `status`、`client_id`、`connected_at` 和 `client_info` 对象。设备的 `status` 显示 `Connected`，有测量值时附带最近一次服务端与客户端之间的心跳往返延迟，例如 `Connected · 203ms`。`client_info` 包含 `hostname`、`platform`、`arch`（客户端进程架构）、`shell`（命令和会话实际使用的解释器路径）、`default_cwd`（未指定 `cwd` 时继承的客户端工作目录）和 `client_version`（握手中上报的客户端应用版本）。元数据字段为空表示未知或客户端未上报。Server 和 client 均需更新才能提供新增元数据。

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

`execute_command` 统一短命令、长任务和交互任务。指定 `session_id` 时，存在则复用，不存在则按此 ID 创建持久终端；已关闭 ID 可以重新创建，`session_created=true` 表示新 shell。省略时每次新建匿名终端：命令在 wait_ms 内完成则自动关闭并释放名额，等待先到期则保留同一 shell 并返回随机 session_id，进程不中断、不重启。同一 token 内 session_id 唯一；多设备创建需要 client_id，已有 ID 可定位所属设备。没有默认终端或隐式复用。

目录、变量、函数和虚拟环境状态在同一 shell 中保留。`cwd` 和 `env` 仅初始化新会话，复用时携带会报错；后续通过 `cd`、`export` 或目标 shell 对应命令修改。每个会话同一时间只执行一条命令，忙碌时返回 `session_busy` 和会话 ID，不自动另开终端、不排队。

`wait_ms` 默认 10000（10 秒），范围 0–20000，从客户端接受开始计时，新匿名终端的 shell 启动时间也计算在内。0 表示立即保留匿名终端，仍需先等待客户端接受回执。等待到期不会终止命令。快速完成返回 `status=completed`，未完成返回 `status=running`、已有输出和 next_cursor，之后通过 session_read 读取、session_control 交互。`persistent=true` 表示命令完成后仍保留 shell；`promoted=true` 表示该终端由匿名终端在首次等待到期后保留而来（包括 0）。具名和保留后的终端没有空闲回收，需要 `session_control(action=stop)` 主动关闭。MCP 等待取消不会取消已接受任务，也不会停止客户端保留计时。已移除 timeout_ms。

终端输出统一为合并的 output。command_done 和 exit_code 描述最新命令；done 表示终端已结束，session_alive 表示可用状态。快速匿名命令返回 `command_done=true`、`done=true`、`session_alive=false`、`persistent=false`、`promoted=false`。具名或已保留终端的命令完成后为 `done=false`、`session_alive=true`、`persistent=true`。命令边界和退出码由客户端 shell 集成报告，不依赖可见提示符猜测。

Linux/macOS 使用上报的 shell 和 PTY；Windows 使用 PowerShell 和管道，支持基本输入交互，不模拟完整终端。为保持命令边界可靠，持续终端不加载用户 shell 启动配置；继承客户端环境，并保留提示符钩子、PS1 等供集成使用，请勿覆盖它们或修改终端回显。需要的环境可以在命令中显式初始化。服务端和客户端均需更新，旧客户端会收到升级提示。

每个客户端进程最多 10 个存活终端，启动、运行、空闲和关闭中的 shell 在实际退出前都占名额。CLI/GUI 共用客户端执行层，同一进程内多个连接共享上限；同一 token 下不同进程各自计数。满额时新建匿名终端、创建尚不存在的具名终端均直接报错；明确指定已有 session_id 仍可复用，不自动选择其他终端。已关闭输出记录不占名额。服务端仅清理输出缓存，不检查终端配额或触发空闲关闭。客户端断线会终止该连接的终端，重连不恢复 shell 状态；服务端前端重启不影响 runtime 中已有连接和终端。

</details>

<details>
<summary>读取输出与查找会话</summary>

`session_read.max_bytes` 默认 16384，范围 4–65536 字节；输出不会在 UTF-8 字符中间切断。每次用返回的 `next_cursor` 继续读取。`has_more=true` 表示当前仍有保留输出；即使 `done=true`，也应读到 `has_more=false`。`command_done` 和 `exit_code` 标识最新命令的结果，`done` 仅表示整个终端已结束。分页本身不会触发 `truncated`。

`session_list` 按创建时间返回本 token 的终端，包括离线设备的保留记录。可按 `client_id` 筛选，`session_id`、`persistent` 和 `promoted` 描述客户端回报的保留状态，移除旧 `default` 字段；`status` 为 `idle`、`running`、`stopping` 或 `finished`。`command`/`command_id` 是最近提交的命令，`command_done` 标识命令完成，`done` 标识终端结束。`output_start_cursor` 指向最早保留输出，`output_end_cursor` 指向末尾。已结束记录最多保留 30 分钟，也可能在新建终端时被替换。

`session_read` 可附带 `command_id` 固定读取的命令；若终端已执行下一条命令则报错，避免把新命令的状态当成旧命令结果。不带此字段仍可按字节游标读取整个会话的保留输出。缓冲区保留最近 1 MiB，较旧输出被丢弃时设置 `truncated=true`，单纯分页不会设置它。命令完成后仍应读到 `has_more=false`。

</details>

<details>
<summary>会话控制：输入、中断、EOF 和停止</summary>

`session_control` 必须提供 `session_id` 和 `action`。`input` 仅用于向当前执行的命令发送输入；空闲终端拒绝输入，新命令使用 `execute_command` 提交。仅 `action=input` 可带且必须带 `input`；仅 `action=stop` 可附带 `wait_ms`（默认 1000）。其他 action 不得带这些参数，混用会被拒绝。每个成功结果均带 `action`，发送输入、控制送达和停止确认使用不同的返回 Schema。由于输入和中断可能重复产生副作用，整个工具保守标注为非幂等；重复 stop 仍按下文语义处理。管理访问可见七个工具；设备范围访问可见六个，不含 `create_client`。

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

</details>
