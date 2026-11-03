# MCP 工具技术参考

当前源码构建会为全部八个工具提供明确的返回 Schema 和行为标注。请以实际连接 Server 的 `tools/list` 响应为准，既有部署或较旧发布的二进制可能仍提供旧版 Schema。

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `client_token`、`invitation`、`expires_at`、平台命令、`display_text`。 |
| `list_clients` | 查看设备或等待连接。 | `clients`；等待时还返回 `status`。 |
| `p2p_transfer_start` | 让远端自动下载并初始化 p2p-cli，注册协商后返回 Agent 端脚本。 | `connection_id`、双方客户端 ID、`state`、`shell_script`、`powershell_script`。 |
| `execute_command` | 运行短时非交互命令并等待结果。 | `stdout`、`stderr`、`exit_code`、`timed_out`、`canceled`、`truncated`，以及可选 `error`。 |
| `session_start` | 启动交互或长时间运行的进程。 | `session_id`、初始 `cursor=0`。 |
| `session_read` | 分页读取输出并检查会话是否完成。 | `output`、`next_cursor`、`has_more`、`truncated`、`done`；终态还包含退出、超时、取消及错误字段。 |
| `session_control` | 用 `action` 选择输入、中断、EOF 或停止。 | `action`；输入返回 `accepted`，中断/EOF 返回 `delivered`，停止返回确认状态及终态字段。 |
| `session_list` | 找回所选 token 的保留会话，可按 `client_id` 筛选，无需设备在线。 | `sessions`：会话、设备、原始启动命令、时间、状态及保留输出游标范围。 |


## 常用调用

创建邀请后，将连接命令私下交给目标用户；设备连接后可执行命令。需要交互或保留 shell 状态时，启动会话并发送输入：

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

工具和字段以已连接服务的 `tools/list` 为准。

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
<summary>命令与交互式会话</summary>

每次 `execute_command` 都启动独立 shell，请显式设置 `cwd` 和 `env`，它们不会跨命令保留。默认超时为 60 秒，`timeout_ms=0` 表示不限时，stdout/stderr 共用 1 MiB 上限。交互提示、增量输出或长任务应使用会话工具，保留 `session_id` 和按字节计数的 `next_cursor`。会话缓冲区只保留最近 1 MiB，出现 `truncated` 时应检查是否丢失较旧输出。

需要保留目录、变量、函数或虚拟环境状态时，用 `session_start` 显式启动常驻 shell（例如 Linux/macOS 上已安装的 `bash -i`），再通过 `session_control(action="input")` 向同一个 `session_id` 发送命令。每次 `session_start` 创建独立进程；一次性命令退出后不会留下常驻 shell。客户端断线会结束会话，重连不会恢复 shell 状态。

</details>

<details>
<summary>读取输出与查找会话</summary>

`session_read.max_bytes` 默认 16384，范围 4–65536 字节；输出不会在 UTF-8 字符中间切断。每次用返回的 `next_cursor` 继续读取。`has_more=true` 表示当前仍有保留输出；即使 `done=true`，也应读到 `has_more=false`。`done` 和 `exit_code` 属于整个会话进程，不能用于判断常驻 shell 中单条命令是否完成。分页本身不会触发 `truncated`。

`session_list` 按启动时间返回本 token 的会话，包含离线设备的保留记录。`output_start_cursor` 指向最早保留输出，`output_end_cursor` 指向当前输出末尾；使用后者读取会跳过已有输出。会话 `status` 为 `running`、`stopping` 或 `finished`，不推断 shell 是否空闲、当前目录或单条命令状态。结束记录保留最多 30 分钟，也可能在新建会话时被替换。

</details>

<details>
<summary>会话控制：输入、中断、EOF 和停止</summary>

`session_control` 必须提供 `session_id` 和 `action`。仅 `action=input` 可带且必须带 `input`；仅 `action=stop` 可附带 `wait_ms`（默认 1000）。其他 action 不得带这些参数，混用会被拒绝。每个成功结果均带 `action`，发送输入、控制送达和停止确认使用不同的返回 Schema。由于输入和中断可能重复产生副作用，整个工具保守标注为非幂等；重复 stop 仍按下文语义处理。管理访问可见八个工具；设备范围访问可见七个，不含 `create_client`。

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
