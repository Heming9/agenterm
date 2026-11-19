# MCP 工具技术参考

[English](mcp-tools.md) · [使用指南](usage.zh-CN.md) · [终端客户端 API](client-api.md)

Schema 和行为标注以所连接服务端的 `tools/list` 为准。管理访问提供全部八个工具，调用设备工具时须传入对应邀请的 `control_token`。范围限定的访问提供七个设备工具，使用该凭据作为 HTTP Bearer token。

| 工具 | 使用场景 |
| --- | --- |
| `create_client` | 创建私密连接邀请。 |
| `list_client` | 查看在线设备或等待设备上线。 |
| `close_client` | 关闭指定设备。省略 `client_ids` 会关闭该令牌下的全部在线设备并撤销令牌和邀请，即使当前没有设备在线也会撤销。 |
| `execute_command` | 在新建或已有终端中执行命令。 |
| `session_read` | 继续读取输出并检查命令状态。 |
| `session_list` | 查找在线设备保留的会话。 |
| `session_control` | 发送输入、中断或 EOF，或停止终端。 |
| `p2p_transfer_start` | 启动远端组件，准备直连传输。 |

## 邀请和设备

`create_client` 在 MCP 文本内容和结构化字段中返回相同的 `display_text`，结构化结果还包含私密凭据 `control_token` 以及 UTC RFC 3339 格式的截止时间 `expires_at`。完整展示 `display_text`，只翻译其中的自然语言，不得改写、摘要、增删或重排内容。保留代码块，逐字复制命令、URL、设备令牌和短码。每个原始 URL 都必须作为可见文本显示，不得替换为 Markdown 链接标题。已知用户时区时，按该时区显示过期时间，否则用 UTC，并标注时区。

将 `control_token` 保存到宿主提供的私密存储，并与本次连接关联，方便跨轮复用，等待超时后也应保留。凭据仅供控制端使用，须避开日志、用户可见消息和邀请材料。受助者使用 `display_text` 中的链接或命令。

`create_client` 的每个可选参数均使用默认值，除非用户明确要求其他值；用户未指定任何设置时，使用空对象调用。邀请默认有效 900 秒。设置 `multi_device: true` 可允许多台设备同时在线。短码启用时，单设备邀请会使用短码链接。复用和兑换规则见[邀请有效期与短码](usage.zh-CN.md#邀请有效期与短码)。在浏览器链接后加上 `.sh` 或 `.ps1` 可获取安装脚本。仅当用户明确要求临时安装时，才设置 `temporary_assistance: true` 创建临时安装邀请，仅支持终端协助。

### 等待设备上线

`list_client` 返回在线设备数组 `clients`，每项包含 `client_id`、`connected_at` 和 `client_info`。使用 `wait_ms: 0` 立即查询。正值会在任一设备在线时提前返回。单次实际等待最多 30 秒，兼容不超过 `300000` 毫秒的旧参数。

1. 展示邀请后，开始按实际经过时间计算 5 分钟预算。使用返回的 `control_token` 分轮调用 `list_client`，每轮 `wait_ms` 取剩余毫秒数，最多 `30000`。
2. `clients` 中出现设备时结束等待。空数组只表示本轮返回时没有设备在线，可在剩余预算内继续查询。
3. 遇到临时网络错误时，用 `wait_ms: 0` 在同一预算内复查一次。复查失败则报告检查失败、设备状态未知。用户取消或凭据出错时结束。凭据无效、过期或撤销需单独说明。
4. 预算耗尽后，再用 `wait_ms: 0` 检查一次。仅当检查成功且数组仍为空时，报告连接等待超时。

### 关闭设备

传入非空且无重复的 `client_ids`，仅关闭这些设备，保留令牌和邀请。所有 ID 都须属于该令牌，校验失败时不会关闭任何设备。省略 `client_ids` 会关闭该令牌下的全部在线设备并撤销令牌和邀请，即使当前没有设备在线也会撤销。返回的 `client_ids` 是本次关闭的设备。关闭设备会结束其会话和进行中的传输。

## 终端会话

多设备令牌调用 `execute_command`、`session_read` 或 `session_control` 时须传入 `client_id`。会话 ID 在所属设备内唯一。

- 用 `setup` 新建终端，可选 `cwd`、`env` 和 `session_id`。显式指定的 `setup.session_id` 须尚未使用，会创建持久终端。
- 用顶级 `session_id` 复用存活终端并保留 shell 状态，不能同时传入 `setup`。终端忙碌时返回 `session_busy`。
- 省略会话 ID 时，执行使用匿名终端。命令先完成则关闭，等待先到期则保留。`wait_ms` 从客户端接纳命令起计时，默认 `10000` 毫秒，范围 `0–20000`。设为 `0` 会立即保留匿名终端。调用方停止等待后，已接纳的命令可以继续执行。

每个客户端进程最多有 10 个存活终端。具名和保留的终端没有空闲到期时间，可复用合适的空闲终端，或在任务结束后主动关闭。已关闭的历史记录不占名额。

| 状态 | 含义 |
| --- | --- |
| `running` | 终端忙碌，或停止请求待确认。 |
| `idle` | 命令已结束，终端可以复用。 |
| `closed` | 客户端已确认 shell 终止，保留的输出可能仍可读取。 |

### 执行、读取和结束

以下示例使用单设备的范围限定访问。

1. 调用 `execute_command`，用 `{"command":"pwd"}` 快速执行，或用 `{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}` 保留具名终端。
2. 命令仍在运行或 `has_more=true` 时，调用 `session_read`，传入返回的 `session_id`，并用 `next_cursor` 作为 `cursor`。传入 `command_id` 后，如果已有新命令启动，读取会报错。即使状态为 `idle` 或 `closed`，也要读到 `has_more=false`。若仍为 `running` 且已读到末尾，可用最多 `20000` 毫秒的 `wait_ms` 等待新输出。
3. 用 `execute_command` 和 `{"session_id":"build","command":"git status"}` 复用空闲终端，或用 `session_control` 和 `{"session_id":"build","action":"stop"}` 停止终端。停止待确认时，查到 `closed` 后才可确认终端名额已释放。

单次执行最多返回 16 KiB UTF-8 输出。具名和保留终端会返回 `session_id`，匿名终端关闭后有更多输出或输出被截断时也会返回该字段。读取始终返回 `session_id`。`max_bytes` 默认 16384，范围 4–65536 字节。读取保证 UTF-8 字符完整。缓冲区保留最近 1 MiB，`truncated=true` 表示请求的部分旧输出已丢弃。命令完成时返回 `exit_code`，成功为 0。传输失败时结果仍待确认。

`session_list` 可按 `client_id` 或状态筛选。用 `idle` 找可复用终端，用 `live` 查 `running` 和 `idle`，用默认值 `all` 包含已关闭历史。每个客户端最多保留 10 条已关闭记录，最长 30 分钟。命令预览最多 160 个 Unicode 字符。临时断线时，会话和结果可保留在客户端。重连后可先查询，再提交新命令。详见[终端客户端 API](client-api.md)。

### 控制会话

`session_control` 要求提供 `session_id` 和 `action`。

- `input` 要求传入 `input` 文本，原样发送给当前命令。需要时自行添加换行，例如 `{"session_id":"build","action":"input","input":"yes\n"}`。
- 发送 `interrupt` 或 `eof` 前，先检查 `list_client.client_info.session_controls`。在 Linux/macOS 上，中断向前台进程组发送 SIGINT，规范模式下 EOF 发送 VEOF。Windows 管道模式支持 EOF，会永久关闭标准输入。
- 仅 `stop` 接受 `wait_ms`（默认 `1000`，范围 `0–20000`）。停止待确认时返回 `running`，确认后返回 `closed`，并按需返回 shell 退出码或错误。可安全重复调用 stop。

输入、中断和 EOF 返回 `ok: true` 表示客户端已确认请求。可继续读取输出，核实程序的响应。投递结果不明时避免重试输入或信号。保留终端中所需的 shell 环境应显式初始化，并保留集成使用的提示符钩子和回显设置。

## 文件传输

普通邀请支持 `p2p_transfer_start`。多设备令牌须传入 `client_id`。`waiting_for_agent` 表示远端组件已注册。按 Agent 的实际执行环境运行对应私密脚本，再用脚本返回的 `executable` 路径查询状态，等到 `connected` 后开始复制。详见 [P2P 组件说明](../components/p2p-cli/README.md)。
