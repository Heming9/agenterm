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

托管服务目前无需管理认证，按邀请创建者的公网 IP 限制最多 4 台在线设备；同一网络下的使用者共享额度。请私密保管邀请和连接 token。需要自行控制管理访问时，使用自托管方式。

## MCP 工具与返回结果

当前源码构建会为全部七个工具提供明确的返回 Schema 和行为标注。请以实际连接 Server 的 `tools/list` 响应为准，既有部署或较旧发布的二进制可能仍提供旧版 Schema。

| 工具 | 使用场景 | 主要返回字段 |
| --- | --- | --- |
| `create_client` | 创建私密连接邀请。 | `client_token`、`invitation`、`expires_at`、平台命令、`display_text`、`next_action`。 |
| `list_clients` | 查看设备或等待连接。 | `clients`；轮询时还返回 `status`、`next_action`。 |
| `execute_command` | 运行短时非交互命令并等待结果。 | `stdout`、`stderr`、`exit_code`、`timed_out`、`canceled`、`truncated`，以及可选 `error`。 |
| `session_start` | 启动交互或长时间运行的进程。 | `session_id`、初始 `cursor=0`。 |
| `session_read` | 增量读取输出并检查是否完成。 | `output`、`next_cursor`、`truncated`、`done`；终态还包含退出、超时、取消及错误字段。 |
| `session_write` | 发送输入；提交提示回答时包含换行符。 | `accepted` 仅表示已发送，不表示程序已读取。 |
| `session_stop` | 请求取消并短暂等待确认。 | `accepted`、`done`、`status`、`already_done`，以及可用的终态字段或 `next_action`。 |

`list_clients` 为每台设备返回 `status`、`client_id`、`connected_at` 和 `client_info` 对象。设备的 `status` 显示 `Connected`，有测量值时附带最近一次服务端与客户端之间的心跳往返延迟，例如 `Connected · 203ms`。`client_info` 包含 `hostname`、`platform`、`arch`（客户端进程架构）、`shell`（命令和会话实际使用的解释器路径）、`default_cwd`（未指定 `cwd` 时继承的客户端工作目录）和 `client_version`（握手中上报的客户端应用版本）。元数据字段为空表示未知或客户端未上报。Server 和 client 均需更新才能提供新增元数据。

每次 `execute_command` 都启动独立 shell，请显式设置 `cwd` 和 `env`，它们不会跨命令保留。默认超时为 60 秒，`timeout_ms=0` 表示不限时，stdout/stderr 共用 1 MiB 上限。交互提示、增量输出或长任务应使用会话工具，保留 `session_id` 和按字节计数的 `next_cursor`。会话缓冲区只保留最近 1 MiB，出现 `truncated` 时应检查是否丢失较旧输出。

`session_stop` 最多等待 `wait_ms` 毫秒取得终态，默认 1000，范围 0–20000；设为 0 时立即返回。按以下方式判断结果：

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

需要纯文本输出时使用 `./agenterm-client -plain`；重定向输入或输出时自动启用。通过 SSH 使用复制功能时，需要终端支持 OSC 52 剪贴板访问。

**桌面客户端（GUI）**支持粘贴 token、连接和断开、查看实时命令与输出，跟随系统明暗主题，并显示连接延迟和命令统计。

客户端在内存中保留最近最多 10 MiB 日志，不保存连接 token。清空日志只影响本地显示。

## 常见问题

### 连接失败或邀请过期

- 确认 Agent 和目标电脑都能访问服务器地址。`127.0.0.1` 仅适用于同一台电脑。
- 邀请过期或已使用时，让 Agent 重新生成邀请。
- Server 重启后，原有邀请和 token 失效，需要重新创建连接。
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
