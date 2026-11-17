# 部署指南

[English](deployment.md) · [返回首页](../README.zh-CN.md) · [使用指南](usage.zh-CN.md)

可以在 Linux 或 macOS 上直接运行服务端，也可以在 Linux 上使用 Docker Compose 或 systemd。目标电脑可使用 macOS、Linux 或 Windows 客户端。

## 📋 准备

- 让 Agent 和目标电脑都能访问部署地址。
- 从 [GitHub Releases](https://github.com/Heming9/agenterm/releases) 下载对应系统和架构的 `agenterm-server`，或按下方步骤从源码构建。Docker Compose 会从源码构建，无需单独下载。

## 🚀 方式一：直接运行

在可信网络中，将 `SERVER_IP` 替换为双方可访问的服务器地址：

```sh
chmod +x agenterm-server
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

首次启动时，终端会显示 MCP 地址和管理密钥。请妥善保存密钥，并按[使用指南](usage.zh-CN.md#接入-agent)将 MCP 地址接入 Agent。

## 🐳 方式二：Docker Compose

需要 Docker 和 Docker Compose。在仓库目录中执行：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
install -m 600 .env.example .env
install -d -m 700 .secrets
(umask 077 && openssl rand -hex 24 > .secrets/management-key)
chmod 444 .secrets/management-key
```

Compose 只将密钥文件挂载到容器的 `/run/secrets/management_key`。宿主目录的 0700 权限限制其他用户访问，文件的 0444 权限允许容器内的服务用户读取。升级时，将原 `.env` 里的 `AGENTERM_MANAGEMENT_KEY` 值单独写入该文件的一行，保留相同权限，再移除旧的环境变量。需要指定其他文件位置时，在 `.env` 中设置 `AGENTERM_MANAGEMENT_KEY_FILE`。

将 `.env` 中的 `AGENTERM_PUBLIC_URL` 设置为双方可访问的服务地址。默认只监听本机，适合通过本机反向代理访问；需要在可信局域网直接访问时，将 `AGENTERM_BIND_ADDRESS` 设置为 `0.0.0.0`。

```sh
docker compose up -d --build
docker compose logs -f server
```

使用日志中显示的 MCP 地址和凭据文件中的管理密钥接入 Agent。重启或重建容器会断开现有连接并清空内存中的会话。

## ⚙️ 方式三：systemd

适用于 Linux，需要 systemd 和已下载的 Linux 服务端程序。在仓库目录中执行，先将下载文件放在当前目录并命名为 `agenterm-server`：

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin agenterm
sudo install -d -m 755 /opt/agenterm
sudo install -m 755 agenterm-server /opt/agenterm/agenterm-server
sudo install -m 644 deploy/systemd/agenterm-server.service /etc/systemd/system/
```

首次安装时创建凭据文件：

```sh
sudo sh -c 'umask 077 && openssl rand -hex 24 > /etc/agenterm-management-key'
```

升级现有部署时，将 `/etc/agenterm.env` 的 `AGENTERM_MANAGEMENT_KEY` 原值单独写入 `/etc/agenterm-management-key` 的一行，将文件属主设为 root、权限设为 0600，再删除环境文件中的密钥项。沿用原值即可复用已有 runtime。若旧 runtime 通过 `-passwd` 启动，原有密钥会保留在该进程的参数中直到进程退出；可在活动会话结束后安排一次 runtime 重启。unit 通过 systemd 的 `LoadCredential` 向服务用户提供凭据文件。

在 `/etc/agenterm.env` 中填写公网 HTTPS 地址：

```text
AGENTERM_PUBLIC_URL=https://YOUR_HOST
```

```sh
sudo chmod 600 /etc/agenterm.env
sudo systemctl daemon-reload
sudo systemctl enable --now agenterm-server
sudo systemctl status agenterm-server
```

服务默认监听 `127.0.0.1:8080`。请确保部署环境提供的访问地址支持 HTTP 和 WebSocket，且 Agent 和目标电脑均可访问。使用设置的地址和密钥接入 Agent。

## ✅ 验证与管理

```sh
# 在服务器上检查健康状态
curl -f http://127.0.0.1:8080/healthz

# Docker Compose 日志
docker compose logs -f server

# systemd 日志
sudo journalctl -u agenterm-server -f
```

接入 Agent 后，创建邀请，在另一台电脑运行邀请命令并授权，确认可以执行终端任务。管理密钥和邀请命令请只分享给信任的人。

## 🔨 从源码构建服务端

需要 Go 1.26.10 或更高版本：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
```

构建后可按“直接运行”或“systemd”方式部署。

## 发布流水线

[发布工作流](../.github/workflows/release.yml) 并行运行[构建检查](../.github/workflows/checks.yml)、六个平台的 Rust CLI 构建和原生 P2P 构建。Server 打包等待 CLI、P2P 分发完成，所有检查通过且客户端下载资源校验成功后才能发布。CLI 发布 macOS、Linux、Windows 的 amd64 和 arm64 程序，Server 发布 macOS 和 Linux 的 amd64 和 arm64 程序；增量发布不会复用旧 Go CLI 或 GUI。

Rust CLI 构建及组件包更新方法见 [Rust CLI 文档](../tools/agenterm-cli/README.md#release-builds)。Linux CLI 使用系统 OpenSSL，当前发布构建基线为 Ubuntu 24.04；目标机器须具备兼容的 glibc 和 OpenSSL 动态库。
原生 P2P 缓存必须精确匹配组件源码、原生验证脚本、打包工作流、Rust 版本、runner 和目标平台的指纹。恢复时还检查组件版本、程序架构和 SHA-256；缓存缺失或无效时完整重建，Cargo 依赖与编译产物另外缓存。

Rust 和原生端到端测试通过后即保存原生缓存，后续 Go 集成测试失败不会导致再次编译 Rust。即使命中原生缓存，也始终测试服务端信令和启动脚本。独立 P2P 工作流使用同一套前置检查；发布流程只有先通过自己的共用检查，才跳过这次重复检查。没有缓存辅助脚本的旧源码仍走完整构建。首次运行仍是冷构建，缓存命中后的实际耗时需在 Actions 中测量。

## 📖 服务端命令与参数

```sh
agenterm-server [start|stop|restart|status] [参数]
agenterm-server -help
```

| 命令 | 说明 |
| --- | --- |
| `start` | 启动或替换服务端，保留运行中的 runtime 和会话；省略命令时默认使用。 |
| `stop` | 停止服务端和 runtime，清除内存中的状态。 |
| `restart` | 使用指定配置重新创建 runtime 并启动服务端；现有连接和内存状态会丢失。 |
| `status` | 查看服务端和 runtime 的状态，不启动进程。 |

`stop` 和 `status` 只接受 `-instance`。其他参数用于 `start` 或 `restart`；修改运行中 runtime 的启动参数需要使用 `restart`。

**从 runtime API 12 升级到 13：** 旧 runtime 与新的短码及认证规则不兼容。启动 API 13 服务端前，请用原有的 API 12 服务端程序停止旧 runtime；若旧程序不可用，按兼容性提示识别并停止旧 runtime。停止 runtime 会断开客户端并清除内存中的凭证和会话，升级后需重新创建连接。

**从 runtime API 13 升级到 14：** 单设备在线凭证及重连规则需要新 runtime。启动 API 14 前，请用原有的服务端程序停止 API 13 runtime。这会断开客户端并清除内存中的凭证和会话，升级后需重新创建连接。

**从 runtime API 7 升级到 8：** 先保留现有 API 7 服务端程序，以相同用户执行 `/path/to/api7/agenterm-server stop -instance <实例名>`，待旧 runtime 停止后，再使用 API 8 程序和预期配置启动。API 8 的 `start`、`stop` 和 `restart` 均无法管理 API 7 runtime；如果没有旧程序，请按兼容性提示识别并停止旧 runtime。停止 runtime 会断开客户端并清除内存中的凭证和会话，升级后需重新创建连接。不要为绕过兼容检查而降级认证或向旧 runtime 发送控制凭证。

| 参数 | 说明 |
| --- | --- |
| `-instance` | 实例名称，用于区分独立服务；默认 `default`。 |
| `-listen` | 服务监听地址；默认 `127.0.0.1:8080`。 |
| `-public-url` | 对外访问地址，默认 `http://127.0.0.1:8080`；只设置地址，不会启用 HTTPS，传输加密由部署环境负责。 |
| `-url-prefix` | 所有接口的 URL 路径前缀；默认 `/agenterm`，使用 `/` 表示根路径。 |
| `-passwd` | 直接指定 MCP 管理密钥。密钥会出现在启动进程的命令行中。 |
| `-passwd-file` | 从文件读取单行 MCP 管理密钥。与 `-passwd` 二选一。启用认证且两项都省略时，首次启动自动生成密钥。 |
| `-no-password` | 允许无需密码访问 MCP 管理接口。与 `-passwd`、`-passwd-file` 互斥。 |
| `-max-clients-per-ip` | 每个邀请创建者 IP 的最大在线客户端数；默认 `0`，表示不限制。 |
| `-client-ip-header` | 用于读取客户端 IP 的可信请求头，头内须为单个 IP；默认留空，使用直连对端 IP。 |
| `-enable-short-code` | 启用短码生成和兑换；默认禁用短码。 |
| `-stdio` | 同时通过标准输入输出提供 MCP 管理接口，信任启动进程的用户。 |

短码查询失败按来源 IP 在浏览器和客户端接口间累计，MCP 管理密钥单独使用相同规则，连接令牌和其他认证接口不进行 IP 限制：连续失败 3 次后等待 2 秒，此后每次失败增加 2 秒，最长等待 10 分钟；等待期间的请求不增加次数。成功兑换短码仅清零短码计数，查看受邀页面不会清零；成功认证管理密钥仅清零管理密钥计数。同类凭证的并发请求共享其限制。
