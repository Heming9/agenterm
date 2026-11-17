# Agenterm 预发布环境

预发布 MCP 入口为 `https://agenterm.net/staging/mcp`，健康检查为 `/staging/healthz`。它使用独立的 `agenterm-staging` 系统用户、systemd unit、7776 回环端口、PrivateTmp、Runtime 和凭据。预发布邀请和连接与正式环境隔离。

[systemd unit](agenterm-staging.service) 依赖 Agenterm 部署工具提供的 `service_manager.py`，部署时随服务端放入 `/opt/agenterm-staging/releases/<release-id>`，由 `current` 指向当前版本。前端更新通过 `systemctl reload agenterm-staging` 保留现有 Runtime。Runtime 代码或启动配置的更新需要重启预发布 Runtime。

管理密钥由 systemd 的 `LoadCredential` 从 `/etc/agenterm-staging-management-key` 读取，文件属主为 root、权限为 0600。升级旧 unit 时，将 `/etc/agenterm-staging.env` 中的 `AGENTERM_MANAGEMENT_KEY` 原值写入该文件，再移除旧环境变量。

当前源码构建的服务端内嵌 `components.tar.xz`，包含 CLI 和 P2P 各六种系统与架构组合。组件打包和校验见[分发说明](../../internal/components/assets/README.txt)。部署固定源码提交，客户端使用预发布服务返回的安装命令获取匹配的程序。首次接入 Nginx 时安装本目录的 [location 配置](nginx.conf)，运行 `nginx -t` 验证后 reload。

2026-11-03 的 `0.5.0-rc.1` 首次部署验证了公网 MCP、Windows 客户端连接及 macOS 与 Windows 间的 P2P 双向传输。验证产物记录在该次部署的 `staging-result.json`。
