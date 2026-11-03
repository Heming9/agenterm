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
cp .env.example .env
openssl rand -hex 24
```

编辑 `.env`，将生成的随机值填入 `AGENTERM_MANAGEMENT_KEY`，将 `AGENTERM_PUBLIC_URL` 设置为双方可访问的服务地址。默认只监听本机，适合通过本机反向代理访问；需要在可信局域网直接访问时，将 `AGENTERM_BIND_ADDRESS` 设置为 `0.0.0.0`。

```sh
docker compose up -d --build
docker compose logs -f server
```

使用日志中显示的 MCP 地址和 `.env` 中的管理密钥接入 Agent。重启或重建容器会断开现有连接并清空内存中的会话。

## ⚙️ 方式三：systemd

适用于 Linux，需要 systemd 和已下载的 Linux 服务端程序。在仓库目录中执行，先将下载文件放在当前目录并命名为 `agenterm-server`：

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin agenterm
sudo install -d -m 755 /opt/agenterm
sudo install -m 755 agenterm-server /opt/agenterm/agenterm-server
sudo install -m 644 deploy/systemd/agenterm-server.service /etc/systemd/system/
```

生成管理密钥：

```sh
openssl rand -hex 24
```

创建 `/etc/agenterm.env`，填入公网 HTTPS 地址和生成的密钥：

```text
AGENTERM_PUBLIC_URL=https://YOUR_HOST
AGENTERM_MANAGEMENT_KEY=YOUR_FIXED_KEY
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

| 参数 | 说明 |
| --- | --- |
| `-instance` | 实例名称，用于区分独立服务；默认 `default`。 |
| `-listen` | 服务监听地址；默认 `127.0.0.1:8080`。 |
| `-public-url` | 对外访问地址，默认 `http://127.0.0.1:8080`；只设置地址，不会启用 HTTPS，传输加密由部署环境负责。 |
| `-url-prefix` | 所有接口的 URL 路径前缀；默认 `/agenterm`，使用 `/` 表示根路径。 |
| `-passwd` | MCP 管理密钥；省略时自动生成，不能与 `-no-password` 同时使用。 |
| `-no-password` | 允许无需密码访问 MCP 管理接口。 |
| `-max-clients-per-ip` | 每个邀请创建者 IP 的最大在线客户端数；默认 `0`，表示不限制。 |
| `-client-ip-header` | 用于读取客户端 IP 的可信请求头，头内须为单个 IP；默认留空，使用直连对端 IP。 |
| `-stdio` | 同时通过标准输入输出提供 MCP 管理接口，信任启动进程的用户。 |
