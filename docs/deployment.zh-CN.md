# 部署指南

[English](deployment.md) · [返回首页](../README.zh-CN.md) · [使用指南](usage.zh-CN.md)

## 启动 Server

只需要构建或下载 **`agenterm-server` 一个程序**，启动命令仍可沿用原来的参数：

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

Server 自动启动或复用内部 Runtime，不需要手动启动第二个进程。Runtime 没有独立的可执行文件、启动命令或部署服务，它的创建、接管、停止和重建统一由 Server 管理。

- **内部 Runtime** 保留公网 TCP 入口，直接管理客户端 WebSocket、认证凭证、邀请、设备和会话；其他 HTTP 请求转给 Server。
- **Server** 提供 MCP、安装脚本和下载，通过私有 Unix Socket 操作 Runtime。

首次启动会显示 MCP 地址；未指定 `-passwd` 时会生成并显示管理密钥。重启 Server 会复用 Runtime 中的原密钥：

```text
URL:            http://SERVER_IP:8080/agenterm/mcp
Authorization:  Bearer 首次启动时显示的管理密钥
```

两个进程以同一用户运行。默认 socket 位于系统临时目录的 `agenterm-<uid>/` 下，目录权限 `0700`，socket 权限 `0600`。内部路径由 `-instance` 自动管理，无需指定 socket 参数。

所有凭证、邀请和会话只在 Runtime 内存中，不写数据库或状态文件。socket 是通信入口，不保存业务状态。公网使用支持 WebSocket 的 HTTPS 反向代理，继续转发到 8080 并保留 `/agenterm` 路径。`-public-url` 不会启用 TLS。托管服务目前要求 Linux/macOS，Windows 目标客户端不受影响。

## 统一组件打包

Client CLI 和 P2P 的平台下载包统一合并为 `internal/components/assets/components.zip`，普通 Go 构建直接将它内嵌到 Server。构建 Server 可执行文件：

```sh
python3 scripts/build_server.py --output dist/agenterm-server --goos linux --goarch amd64
```

只需部署这一个 Server 文件。启动时校验 ZIP 清单、大小和 SHA-256，再原子解压到实例运行目录下的 `components/<包哈希>/`；CLI 与 P2P 都从这里提供下载，不需要旁置 `p2p-assets`。已有缓存会重新校验后复用，不同包分目录保存以保留旧 frontend 的资源。systemd PrivateTmp 和 Docker 的只读根文件系统均可继续使用，因为组件目录位于可写的运行目录中。启动过程不重新压缩下载包，不需要 Python、Rust 或系统解压工具。

## 命令和参数

```sh
agenterm-server [start|stop|restart|status] [参数]
```

不写子命令时默认 `start`。仅保留以下九个参数：

| 参数 | 默认值 | 用途 |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8080` | 对外 TCP 监听地址 |
| `-public-url` | `http://127.0.0.1:8080` | 双方可达的公开地址 |
| `-url-prefix` | `/agenterm` | 品牌及部署路径前缀，`/` 表示根路径 |
| `-passwd` | 首次创建 Runtime 时自动生成 | MCP 管理密钥 |
| `-no-password` | `false` | 明确开启免密管理 |
| `-stdio` | `false` | 同时提供 stdio MCP |
| `-max-clients-per-ip` | `0`，不限 | 每个邀请创建者 IP 的在线设备限额 |
| `-client-ip-header` | 空 | 使用可信代理提供的单个来源 IP；为空时使用直接连接 IP |
| `-instance` | `default` | 区分同一用户运行的多个实例 |

实例名支持 1–40 个小写字母、数字、连字符或下划线，首位需为字母或数字。不同实例需使用不同的监听端口，并配置对应的公开地址。`stop` 和 `status` 只接受 `-instance`。

| 操作 | 行为 |
| --- | --- |
| `agenterm-server` 或 `agenterm-server start` | 自动创建或复用 Runtime；新版 Server 就绪后自动切换入口并排空旧版 |
| Ctrl+C / SIGTERM | 最多等待 70 秒排空 Server 请求；Runtime 保留，等待接管 |
| `agenterm-server status` | 检查当前实例，不创建目录或启动进程；完全就绪返回退出码 0，否则非 0 |
| `agenterm-server stop` | 完整停止当前实例的 Server 和 Runtime，清空内存状态 |
| `agenterm-server restart` 加完整启动参数 | 完整停止后按本次配置重建；连接和会话会清空 |

运行中的 Runtime 保留原配置。再次启动时，未指定的 Runtime 参数不会覆盖已有值；显式指定的值若有冲突则报错。改变这些参数需使用 `restart` 并重新提供完整配置。首次生成的管理密钥也会随 Runtime 复用。

```sh
# 首次启动，或运行新版 Server 接管原 Runtime。
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080 -passwd YOUR_KEY

# 检查状态、完整停止。
./agenterm-server status
./agenterm-server stop

# 明确修改 Runtime 配置：会清空原凭证、连接和会话。
./agenterm-server restart -listen 0.0.0.0:8080 \
  -public-url http://SERVER_IP:8080 -passwd YOUR_KEY -max-clients-per-ip 8

# 单独运行并管理测试实例。
./agenterm-server -instance staging -listen 127.0.0.1:8081 -public-url http://localhost:8081
./agenterm-server status -instance staging
./agenterm-server stop -instance staging
```

Runtime 脱离终端进程组，启动信息回传后关闭启动管道；Server 退出或崩溃后，它会继续保留连接和任务，直到 Server 管理命令明确停止它或机器重启，不设置自动回收超时。

新版 Server 在自己的临时 socket 上就绪后，自动切换内部入口，再让旧版排空在途请求。不再需要手动指定或切换 socket。已经运行的 Runtime 代码不会被替换；确实需要升级它时使用 `restart`。Server 已经退出时，Runtime 会继续缓存会话输出，新的 MCP 请求暂时返回 503，直到 Server 再次启动。

Server 接管或停止 Runtime 前会检查内部 API 版本；API 版本相同时，发布版本可以不同。不兼容的 Server 以非零退出码退出，报错显示双方发布版本、API 版本、Runtime PID 和处理命令，不重启 Runtime，也不切换旧 Server。该不兼容可执行文件的 `stop`、`restart`、`status` 同样会拒绝操作。

请使用兼容版本的 Server 停止同一实例，例如 `/path/to/compatible/agenterm-server stop -instance staging`，再启动新版本。也可以根据报错提示，以相同用户在同一主机或容器内执行 `kill -TERM <PID>`。停止 Runtime 会断开客户端连接，并清空内存中的凭证和会话。旧 Runtime 未报告的发布版本或 PID 会显示“未知”，手动发送信号前应先确认 PID。

首次从旧版单进程架构迁移需要重新邀请连接。交互会话可通过原 `session_id`、游标和 `session_list` 恢复；同步命令在 Server 崩溃后继续执行，但已中断的 MCP 响应无法自动恢复，确认任务状态后再决定是否重试。

旧版参数迁移：`-stop` 改为 `stop`，`-restart-runtime` 改为 `restart`，`-check` 改为 `status`。三个 socket 参数合并为 `-instance`；退出等待时间固定为 70 秒。`-generate` 已删除，邀请统一通过 MCP `create_client` 创建。如果旧实例曾使用自定义 socket 路径，应先用旧版命令完整停止它，再切换到新实例命名方式。

## 使用 Docker

Compose 只声明一个 Server 服务，由 Server 在容器内管理 Runtime。没有单独的 Runtime 镜像、容器或命令。非 root 容器通过 `/tmp` 的 tmpfs 放置通信 socket，业务状态仍只在内存中，无需宿主机目录或 Docker socket。

```sh
cp .env.example .env
openssl rand -hex 24
```

编辑 `.env`，设置 `AGENTERM_MANAGEMENT_KEY` 和双方可达的 `AGENTERM_PUBLIC_URL`：

```sh
docker compose up -d --build
docker compose logs -f server
```

默认在 `127.0.0.1` 发布 8080；可信局域网直连可设置 `AGENTERM_BIND_ADDRESS=0.0.0.0`。容器中的 HTTPS 代理转发到 `server:8080`。

**重启或重建整个容器会同时结束内部 Runtime，无法保留内存连接。** 普通的 `docker compose restart/up/down` 属于完整容器生命周期，不等同于宿主机上只替换 Server 进程。需要跨 Server 更新保留连接时，使用下面的宿主机/systemd 部署方式。

## 使用 systemd

仓库只提供 [Server unit](../deploy/systemd/agenterm-server.service)。创建 `agenterm` 用户，将唯一的 Server 二进制放到 `/opt/agenterm/`，将 unit 放到 `/etc/systemd/system/`，创建仅管理员可读的 `/etc/agenterm.env`：

```text
AGENTERM_PUBLIC_URL=https://YOUR_HOST
AGENTERM_MANAGEMENT_KEY=YOUR_FIXED_KEY
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now agenterm-server
```

更新时原子替换 Server 二进制，再执行 `sudo systemctl restart agenterm-server`。unit 使用 `KillMode=process`，只停止主 Server，内部 Runtime 留给新版接管。

完整关闭时，通过 Server 管理命令停止内部 Runtime：

```sh
sudo systemctl stop agenterm-server
sudo -u agenterm /opt/agenterm/agenterm-server stop
```

临时目录存放通信 socket、入口链接，以及默认的 Runtime 运行日志；凭证和终端输出仍只在内存中。修改 Runtime 参数时，需要先按上述命令完整停止，再更新环境配置并启动服务。

## 接入 stdio MCP 客户端

Agent 仍可只启动一个程序，Runtime 会自动启动或复用：

```json
{
  "mcpServers": {
    "agenterm": {
      "command": "/absolute/path/agenterm-server",
      "args": ["-stdio", "-listen", "0.0.0.0:8080", "-public-url", "http://SERVER_IP:8080"]
    }
  }
}
```

stdio 信任本地进程用户，提供管理权限；HTTP 管理接口仍需认证。stdout 只输出 MCP 消息，启动信息和管理密钥写入 stderr。关闭 stdio 后 Server 退出，Runtime 保留；下次启动自动接管。相同的真实监听 socket 同时只能由一个 Server 使用。

## 日志与线上排错

Server 和 Runtime 使用 Go 标准库 `log/slog`，日志包含 `time`、`level`、`msg`、`component`、`version`、`pid` 和 `instance`。日志配置通过环境变量设置，不增加命令行参数：

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `AGENTERM_LOG_FORMAT` | `text` | `text` 供本地阅读；生产环境使用 `json` |
| `AGENTERM_LOG_LEVEL` | `INFO` | `DEBUG`、`INFO`、`WARN`、`ERROR` |
| `AGENTERM_LOG_FILE` | 空，即 Server stderr | Server 的绝对日志文件路径；父目录需预先创建 |
| `AGENTERM_RUNTIME_LOG_FILE` | 实例目录下的 `runtime.log` | Runtime 独立日志的绝对路径，或显式设置 `stderr` 交给平台收集 |

默认 Runtime 日志位于系统临时目录的 `agenterm-<uid>/runtime.log`；非默认实例位于 `agenterm-<uid>/<instance>/runtime.log`。临时目录路径过长时 IPC 目录会回退到 `/tmp`。文件以 `0600` 权限追加写入，不包含业务状态。目录被系统清理时日志可能丢失，长期保留应使用平台收集或指定持久目录。默认文件不自动轮转；文件模式应配置运维工具轮转，例如 logrotate 的 `copytruncate`，并限制保留量。不要只重命名文件：Runtime 会继续写原文件描述符。

Runtime 默认不继承终端或 MCP 父进程的输出管道；Server 退出、崩溃或更新后，它仍能独立写日志。Compose 已配置两个组件输出 JSON 到容器日志，使用 `local` 驱动轮转（每个文件 10 MiB，最多 5 个文件）；systemd unit 配置 JSON 输出到 journald，保留量由主机 journald 配置控制：

```sh
docker compose logs --since 15m -f server
sudo journalctl -u agenterm-server --since '15 minutes ago' -f -o cat
```

`stderr` 适用于生命周期独立于 Server 的 Docker/journald 收集器。通过 stdio MCP 启动时应保留默认独立文件，避免长期占用启动方的 stderr 管道。普通 Server 更新保留 Runtime，因此修改 Runtime 的日志级别、格式或出口需要明确执行 `restart`，这会断开设备并清空会话；只更新 Server 不会改变原 Runtime 的日志配置。

每个公网 HTTP 请求生成新的 `X-Request-ID` 并回传，覆盖调用者传入的值。该 ID 在 Server、私有 Runtime RPC、MCP 工具以及命令/会话生命周期日志之间传递；stdio 工具调用也会生成关联 ID。可用 `request_id` 串联一次请求，用 `client_id`、`command_id`、`session_id` 继续查询设备和任务事件。命令 ID 与发往客户端的执行消息 ID 一致。成功的健康检查和私有 RPC 记为 DEBUG；失败的请求仍以 WARN/ERROR 记录。

日志记录状态、耗时、退出码、超时/取消标记、WebSocket 关闭码和错误类型。代理连接失败额外记录底层错误，HTTP panic 保留栈但不记录 panic 值。路由使用模板，如 `/c/{code}`，不记录安装码、原始 URL、查询参数、认证头、命令正文、环境变量、会话输入或命令输出；敏感字段还有统一脱敏保护。不要把凭证拼进日志消息或任意错误字符串中。首次自动生成的管理密钥仍只经启动提示交付，因此生产部署应像 Compose/systemd 示例一样显式提供固定管理密钥。

排错时先查 `status` 和两个健康接口：

- `/livez` 失败：先查 Runtime 的启动、退出和资源状态。
- `/livez` 成功、`/healthz` 失败：用响应中的 `X-Request-ID` 查询 `server proxy failed` 或 `runtime health check failed`，区分业务进程、私有 socket 和内部 API 问题。
- 设备断连：查询 `client disconnected`、`client heartbeat failed` 或 `client output queue full`，再用 `client_id` 关联命令和会话。
- 命令超时或失败：查询 `command completed` / `session completed` 的 `exit_code`、`timed_out`、`canceled`，再检查客户端可见输出。

这些配置提供日志定位能力；仓库尚未配置集中日志服务、外部监控探测或告警通知。

## 健康检查

```sh
./agenterm-server -h
./agenterm-server status
```

`GET /livez` 检查内部 Runtime；`GET /healthz` 检查 Server 和内部 API。Server 不会自动重启不兼容的 Runtime。配置可信代理 IP 头时使用 `-client-ip-header`，并确保入口只接受会覆盖该头的可信代理。

## 从源码构建

需要 Go 1.26.10+：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client-cli ./cmd/agenterm-client-cli
```

构建桌面客户端需要 CGo 和 C 编译器；Linux 还需安装图形开发库，参见 [Fyne 环境配置](https://docs.fyne.io/started/)。

```sh
go build -o agenterm-client-gui ./cmd/agenterm-client-gui
```

CLI 和 GUI 使用独立的入口包，共用连接与启动逻辑位于 `internal/client`。发布产物分别使用 `agenterm-client-cli-<平台>-<架构>` 和 `agenterm-client-gui-<平台>-<架构>` 前缀。选择性发布如果以旧产物名称的 Release 为基线，发布计划会自动完整构建，让整套产物一起迁移。

普通源码构建默认内嵌 `components.zip`，其中 Client CLI 与 P2P CLI 均覆盖 macOS、Linux、Windows（amd64/arm64）六种组合，并提供两条一键连接命令。下载地址自动使用 `internal/version.Current`，无需额外发布标记或链接参数。Server 单个二进制即可提供安装脚本、客户端压缩包和校验清单。

修改 CLI 或共享客户端代码后，重新生成资源包，并随源码改动一起提交：

```sh
go generate ./internal/components
go build -o agenterm-server ./cmd/agenterm-server
```

重新生成需要 Python 3.11+ 和 Go，会交叉编译六个平台 CLI，复用现有统一包中的 P2P 归档，并原子替换 `internal/components/assets/components.zip`。更新 P2P 时，先为六种系统与架构组合分别运行 `scripts/build_p2p_cli.py`，再执行 `python3 scripts/build_components.py --p2p-assets <原生构建目录...>` 合并；缺少任意 P2P 平台时打包直接失败。发布工作流会自动串联六平台原生 P2P 构建、合并和 Server 内嵌。资源包缺失时直接编译失败，避免静默关闭组件下载功能。
