# Agenterm 预发布环境

预发布入口为 `https://agenterm.net/staging/mcp`，健康检查为 `/staging/healthz`。使用独立的 `agenterm-staging` 系统用户、systemd unit、7776 回环端口、PrivateTmp、Runtime 和管理密钥。正式环境的 binary、unit、Runtime 和凭据不参与切换。

`agenterm-staging.service` 使用与正式环境相同的 supervisor；后续更新通过 `systemctl reload agenterm-staging` 保留其 Runtime。修改 Runtime 代码时，须明确按预发布的独立生命周期更新，不能把仅 frontend reload 当作 Runtime 升级。首次搭建记录在对应 release 的 `staging-result.json`。

管理密钥保存于服务器 `/etc/agenterm-staging.env`（root 0600）；不要提交到仓库或粘贴到日志。客户端必须从预发布返回的安装脚本下载，以保证版本和 P2P capability 匹配。预发布连接、Token 和邀请与正式环境隔离。

新源码每次构建将六平台 Client CLI 安装资源和六平台 P2P 归档统一合并为 `components.zip` 并内嵌 Server，启动时自动解压到实例运行目录 `components/<包哈希>/`。每次部署固定源码提交 SHA，并校验 P2P 清单及 macOS、Windows 各架构的 2 MiB 下载体积上限。使用独立发布目录 `/opt/agenterm-staging/releases/<release-id>`，`current` 指向当前版本。初次接入 Nginx 仅增加本目录的 location include，先执行 `nginx -t` 后 reload；不重启正式 Agenterm 服务。

2026-11-03 首次部署版本为 `0.5.0-rc.1`，应用源码快照为 `d20ebecb9b01c5eab048d8050c659e1e1ad2948a`。已通过公网 MCP 认证与 8 个工具发现、新版 Windows Client 下载及连接，以及 macOS 与 Windows 跨网络 P2P 双向文件传输和落盘校验。部署前后正式服务的三个核心进程 PID、启动时间、Runtime socket inode 和 unit 哈希均一致，已有 Windows 客户端的 ID 与连接时间保持不变。

首次部署的旧快照只包含 macOS arm64 和 Windows amd64 P2P 归档。仓库中的新版统一包已补齐六种组合；该环境需要单独更新部署才能使用新增平台。
