# 部署指南

[English](deployment.md) · [返回首页](../README.zh-CN.md) · [使用指南](usage.zh-CN.md)

## 启动 Server

从 [Releases](https://github.com/Heming9/agenterm/releases) 下载适合系统的 Server。以下示例用于可信局域网：

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

将 `SERVER_IP` 换成 Agent 和目标电脑都能访问的服务器地址。Server 启动后会显示 MCP 地址和管理密钥，在 Agent 的远程 MCP 配置中填写：

```text
URL:            http://SERVER_IP:8080/agenterm/mcp
Authorization:  Bearer 服务器显示的管理密钥
```

接入后，按[首页的邀请与授权流程](../README.zh-CN.md#快速开始)连接目标电脑。公网部署请配置支持 WebSocket 的 HTTPS 反向代理；`-public-url` 只声明对外地址，本身不会启用 HTTPS。

已有连接 token 时，也可以直接启动 CLI 或 GUI 客户端并粘贴 token：

```sh
./agenterm-client
```

Server 和客户端建议使用同一发布版本。Windows 下运行对应的 `.exe` 文件。

## 使用 Docker

容器只运行 Server，客户端仍运行在需要协助的电脑上，无需挂载宿主机目录或 Docker socket。镜像和独立 Server 一样，内含一键安装脚本和六个平台的 CLI 下载资源。

安装 Docker 和 Compose 插件后，在仓库根目录执行：

```sh
cp .env.example .env
openssl rand -hex 24
```

编辑 `.env`：将生成的值填入 `AGENTERM_MANAGEMENT_KEY`，并将 `AGENTERM_PUBLIC_URL` 设为 Agent 和目标电脑都能访问的地址。默认的 `http://localhost:8080` 仅适用于双方都在 Docker 宿主机上使用的情况。随后启动：

```sh
docker compose up -d --build
docker compose logs -f server
```

Agent 的 MCP 地址为 `AGENTERM_PUBLIC_URL` 加上 `/agenterm/mcp`，认证头为 `Authorization: Bearer 你的管理密钥`。请妥善保管 `.env`，它已被 Git 和镜像构建上下文排除。Compose 要求填写非空管理密钥，因此重建容器后管理认证保持不变。

Compose 默认只在 `127.0.0.1` 发布 8080 端口。可信局域网直连时，将 `AGENTERM_BIND_ADDRESS` 设为 `0.0.0.0`，并在 `AGENTERM_PUBLIC_URL` 使用 Docker 宿主机的局域网 IP。公网使用时，配置支持 WebSocket 的 HTTPS 反向代理，并填写对应的公网 URL。如果反向代理也运行在容器里，需要共享 Docker 网络并转发到 `server:8080`；代理容器里的 `127.0.0.1` 指向代理自身。转发时保留 `/agenterm` 路径。

不使用 Compose 时，也可以手动构建和启动：

```sh
docker build -t agenterm-server:local .
docker run -d --name agenterm-server --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -p 127.0.0.1:8080:8080 agenterm-server:local \
  -listen 0.0.0.0:8080 -public-url http://localhost:8080
docker logs agenterm-server
```

这种方式每次重启会生成并显示新的管理密钥；追加 `-passwd 你的管理密钥` 可以固定密钥，也可以使用上面的 Compose 配置。其他[启动选项](#启动选项)同样可作为容器参数传入。覆盖参数时保留 `-listen 0.0.0.0:8080`，确保 Docker 能将请求转发到 Server。

镜像采用[多阶段构建](https://docs.docker.com/build/building/multi-stage/)、静态 Go 二进制和非 root 运行用户，不需要可写文件系统或数据卷。邀请、客户端凭据和会话都保存在内存中，Server 重启后会清空，需要重新生成邀请并连接客户端。请运行单个 Server 实例，多个独立副本之间不会共享连接状态。

通过 Buildx 构建 Linux amd64 和 arm64 镜像，将示例仓库地址替换为你自己的地址：

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -t YOUR_REGISTRY/agenterm-server:YOUR_TAG --push .
```

更新时切换到需要的源码版本，再执行 `docker compose up -d --build`；停止服务用 `docker compose down`。

## 接入 stdio MCP 客户端

此选项需要从当前源码构建，v0.3.1 发布的二进制尚未包含 stdio 支持。

如果 Agent 通过启动本地进程接入 MCP，可以使用以下配置，将程序路径和 `SERVER_IP` 替换为实际值：

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

stdio 信任启动该进程的用户，直接提供管理权限，无需填写 Bearer 认证头；HTTP 管理接口仍保留认证。HTTP 监听继续提供邀请兑换、安装资源和目标客户端 WebSocket，目标电脑需要能访问配置的公开地址。公网部署请按前文配置 HTTPS 反向代理。

此模式下 stdout 只输出 MCP 消息，启动信息写入 stderr。stdio 连接关闭后，HTTP 服务也会停止。按回车重新生成邀请的交互快捷方式只在纯 HTTP 模式下启用。

## 启动选项

查看可用启动选项：

```sh
./agenterm-server -h
./agenterm-client -h
```

- 用 `--passwd` 设置固定的管理密钥，避免每次启动重新配置 Agent。
- 用 `-url-prefix` 自定义部署路径，Agent 配置和反向代理路径需同步调整。
- 用 `--max-clients-per-ip` 限制每个邀请创建者 IP 的在线设备数量。
- 反向代理后需要识别访客 IP 时，用 `--client-ip-header` 指定代理覆盖的请求头，并限制 Server 只接受可信代理访问。

## 从源码构建

需要 Go 1.26.10+：

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

构建桌面客户端需要 CGo 和 C 编译器；Linux 还需安装图形开发库，参见 [Fyne 环境配置](https://docs.fyne.io/started/)。

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
```

普通源码构建默认内嵌随仓库提供的 macOS、Linux、Windows（amd64/arm64）六个平台 CLI 资源包，并提供两条一键连接命令。下载地址自动使用 `internal/version.Current`，无需额外发布标记或链接参数。Server 单个二进制即可提供安装脚本、客户端压缩包和校验清单。

修改 CLI 或共享客户端代码后，重新生成资源包，并随源码改动一起提交：

```sh
go generate ./internal/installer
go build -o agenterm-server ./cmd/agenterm-server
```

重新生成需要 Python 3 和 Go，会交叉编译六个平台 CLI，并原子替换 `internal/installer/assets/clients.tar.xz`。普通构建不需要 Python 或 XZ，运行 Server 的机器也不需要安装 Go。资源包缺失时直接编译失败，避免静默关闭一键连接功能。
