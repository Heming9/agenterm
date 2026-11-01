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
