# p2p-cli

Rust 实现的独立 P2P 文件传输组件。`start` 建立并维护一条后台连接，`status` 查看连接，`cp` 在同一连接上双向复制文件，`stop` 通知双方断开。文件只走加密直连，不回退到服务器中转。

## 通过 Agenterm 初始化

Server 和 Client 都需要支持此功能的源码版本。P2P 和 Client CLI 的下载资源统一内嵌到 Server 的 `components.tar.xz`；现有线上部署不会因本地源码变化自动获得新组件。

1. Agent 调用 `p2p_transfer_start`，按现有认证规则提供目标设备的 `client_id`，管理调用还需 `client_token`。
2. Client 执行内置脚本，自动下载、校验并以前台受控进程运行远端 `p2p-cli`。
3. 远端注册协商后，MCP 返回 `shell_script` 和 `powershell_script`，状态为 `waiting_for_agent`。此时不等待直连成功。
4. Agent 按自己的执行平台运行对应脚本，自动获取本机组件并启动后台连接。脚本输出 JSON，包含组件的 `executable` 绝对路径。
5. 使用该绝对路径执行下面的命令，避免依赖当前 shell 的 PATH。`status` 返回 `connected` 后再复制文件。

返回脚本包含短期配对凭据，应通过现有私有工具通道执行，不公开分享。远端与 Agent 的操作系统可以不同，不能用远端系统推断 Agent 应执行哪种脚本。

## 四个命令

```sh
p2p-cli start --config negotiation.json
p2p-cli start --config -                  # 从 stdin 读取 JSON
p2p-cli status
p2p-cli status --verbose                 # 完整诊断信息，也可用 -v
p2p-cli cp 'agent-ID:/path/source.txt' 'client-ID:/path/destination.txt'
p2p-cli cp 'client-ID:/path/result.txt' 'agent-ID:/path/result.txt'
p2p-cli cp 'agent-ID:/path/source.txt' 'client-ID:/path/existing.txt' --overwrite
p2p-cli stop
```

- 一位操作系统用户同时维护一条连接。相同连接重复 start 返回已有状态；不同连接需要先 stop。
- `status` 默认只显示本机状态（`state`）、双方 ID 和主机名、对方状态（`peer.state`），以及当前复制进度和速率（`copy.bytes`、`copy.total`、`copy.bytes_per_second`；空闲为 `null`）。`status --verbose` 或 `status -v` 额外显示连接标识、系统、架构、工作目录、最近通信时间、程序路径和完整复制信息，其中 `copy.average_bytes_per_second` 为本次任务平均速率。速率单位均为字节/秒（B/s）；实时速率按约 3 秒滑动窗口计算，每 100 毫秒采样，停滞后会降到零；开始不足 3 秒时使用已有采样。发送端按对方已确认的字节数计算，接收端按已写入的字节数计算；平均速率包含本次任务从请求到查询时的耗时，完成回执和 `cp` 进度输出也包含速率。失败原因在两种模式下都显示，进程退出后仍可查询；本机状态只保留顶层 `state`，不重复输出 `connection.local`。
- `cp` 只支持单个普通文件，源和目标必须是本机与已连接的对端。客户端 ID 来自 MCP 返回值或 status。Agent ID 为 `agent-<token 字符串最后 6 位>`，直接截取、不解析公开标识字段，同一 token 下保持一致；远端为 `client-<client_id>`，每条 P2P 连接仍有独立的 `connection_id` 和私有凭据，显示 ID 不用于鉴权。路径按第一个冒号分隔，Windows `client-ID:C:\Users\name\file.txt` 的盘符保持完整；含空格的整个操作数需要引号。相对路径以对应后台进程的工作目录为准，建议使用绝对路径。
- 默认拒绝覆盖。`--overwrite` 会先写入并校验临时文件，然后原子替换，原文件保留到提交成功。目录和目标符号链接拒绝写入。
- 同一连接一次复制一个文件，可以连续、双向执行多次 cp。完成结果包含字节数、SHA-256 和 `receiver_committed`。
- `stop` 取消当前复制，清理未提交临时文件，并通知对端关闭连接。返回的 `peer_confirmed` 表示是否收到对端确认；对端无法到达时不声称确认成功。已经提交的文件保留。
- 初始化脚本结束不影响 Agent 端后台连接；Client 退出或断开时，先请求其受控组件停止，再回收进程树。异常强杀或掉电仍可能留下临时文件；对端通过保活检测断连。

## 协商参数与协议

组件仅依赖协商入口和 `v1` 协议。最小配置如下，ID 与凭据由 `p2p_transfer_start` 产生：

```json
{
  "signal_url": "https://server.example/agenterm/p2p",
  "session": "32个十六进制字符的连接标识",
  "token": "该端点的私有十六进制接入凭据"
}
```

组件向 `/v1/{session}/register` 注册，从响应获取自己的客户端 ID、协商角色与 STUN 配置。两端凭据不同；只有指定的一端能写 offer，另一端能写 answer。SDP 写入幂等、不可覆盖，每条上限 128 KiB。协商角色与文件方向独立。

只接受 HTTPS，测试时仅对回环地址允许 HTTP，不跟随重定向。连接建立后文件复制和保活直接走 DataChannel；Server 不参与文件数据传输。当前支持 IPv4，候选收集最多等待约五秒；IPv6、目录复制、断点续传和网络切换后自动重连尚未实现。

可选配置包括 `timeout_seconds`（建连及每次复制的期限，默认 600，范围 1–3600 秒）、`max_bytes`（默认 10 GiB）、`cancel_file` 和 `bind_ip`。测试夹具可显式设置 `register: false` 并提供 `client_id`、`offerer` 和 `stun`，直接测试 SDP 邮箱；正常 Agenterm 初始化不需要这些测试参数。

本地命令使用仅限回环的、带随机凭据的命令通道，凭据保存在用户私有状态目录。默认目录为 `~/.p2p-cli`；测试可用 `--state-dir DIR` 或 `P2P_CLI_STATE_DIR` 隔离不同实例。`start --foreground` 用于 Client 管理子进程。初始化及停止脚本使用连接标识限定清理目标，不会误停之后创建的新连接。

## 构建与 Server 分发

从仓库根目录执行，需要 Rust、Python 3.11+ 及 Rust 依赖所需的本机编译工具：

```sh
python3 scripts/build_p2p_cli.py --output dist/p2p-assets
```

脚本原生构建 release 程序，生成 `manifest.json` 和带平台、架构名称的原始可执行文件。统一分发覆盖三种系统的 amd64、arm64，共六种组合。清单绑定平台、架构、版本和 SHA-256。不同版本不能混装。

交叉编译时显式传入 `--target <Rust target triple>`；已有二进制可再指定 `--binary <路径>`。脚本核对 ELF、Mach-O 或 PE 头中的系统与架构，拒绝错误标记的程序。Linux 分发使用 `x86_64-unknown-linux-musl` 和 `aarch64-unknown-linux-musl` 静态链接目标，避免依赖目标机器的 glibc 版本；原生构建机需安装目标 Rust 标准库和 `musl-tools`。

Windows 构建使用 `-C target-feature=+crt-static` 静态链接 C 运行库，目标电脑无需另装 Visual C++ Redistributable；直接运行 Cargo 时也需通过 `RUSTFLAGS` 指定此选项。

macOS 和 Windows 的首选 XZ 下载包限制为 2 MiB，原生构建时检查；兼容格式和直接下载的原始程序可以更大。release 使用 `panic = "abort"` 减少栈展开代码，普通错误仍按原有路径处理和清理；Rust panic 会直接终止进程，可能留下未提交临时文件。

将所需平台的原生构建结果合并：

```sh
python3 scripts/build_p2p_cli.py --merge build-macos-* build-windows-* build-linux-* --require-all --output dist/p2p-assets
```

将 P2P 原生构建产物与 Client CLI 一起生成统一包并内嵌到 Server：

```sh
python3 scripts/build_components.py --p2p-assets dist/p2p-assets
python3 scripts/build_server.py --output dist/agenterm-server
```

唯一内嵌资源为 `internal/components/assets/components.tar.xz`，内部包含 `client-cli/`、`p2p-cli/` 的原始程序和全包校验清单，在同一个 XZ 流中整体压缩。生成统一包时要求 P2P 六种组合齐全，缺少任意一种直接失败。Server 启动时只把原始程序和源文件校验清单原子解压到实例运行目录的 `components/<包哈希>/`，不需要额外部署组件目录；组件下载路由继续使用公开路径前缀。收到下载请求后，Server 才按目标平台和格式打包，合并同一包的并发构建并缓存结果。客户端实际探测解压能力：macOS/Linux 优先 `tar.xz`、其次 `tar.gz`；Windows 优先 `tar.xz`、其次 ZIP；缺少解压能力时直接下载源程序。每种方式最终都核对同一个源程序 SHA-256，无需额外安装解压工具。Server 内置 Go XZ 编解码器。缺少目标平台归档或校验不通过时，MCP 明确返回错误。修改 CLI 可运行 `go generate ./internal/components`，它会复用统一包中既有的 P2P 原始程序。

`P2P_CLI_STUN` 在 Runtime 启动时配置 STUN 主机与端口，默认 `stun.l.google.com:19302`，本机测试设为 `none`。它由协商协议返回给组件，组件使用者无需另行配置；更改已有 Runtime 的环境配置需要按既有规则重启 Runtime。

`.github/workflows/p2p-cli.yml` 为 Linux、macOS、Windows 的 amd64、arm64 分别指定原生 runner，六个任务各自执行单测、命令端到端测试和真实初始化链路验证，再合并完整分发产物。此工作流只构建和上传工作流产物，不自动发布或部署服务。

## 验证

```sh
cargo test --manifest-path tools/p2p-cli/Cargo.toml --locked
cargo clippy --manifest-path tools/p2p-cli/Cargo.toml --all-targets -- -D warnings
python3 tools/p2p-cli/daemon_e2e.py tools/p2p-cli/target/release/p2p-cli
# 单独运行 45 秒延迟启动回归
python3 tools/p2p-cli/daemon_e2e.py tools/p2p-cli/target/release/p2p-cli --delayed-only
```

真实 MCP、脚本下载、双向复制与 Client 退出清理测试：

```sh
P2P_CLI_TEST_BINARY="$PWD/tools/p2p-cli/target/release/p2p-cli" \
P2P_CLI_TEST_ASSETS="$PWD/dist/p2p-assets" \
go test ./internal/service -run TestP2PBootstrapEndToEnd -count=1
```

Windows 使用相应 `.exe` 路径。单元测试覆盖 SDP 兼容、拒绝中转、STUN、Windows 路径、校验失败、目标写入竞争和覆盖提交；持久连接测试覆盖连续双向复制、空文件、块边界、32 MiB 与 256 MiB 文件、大小限制、拒绝目录、未经授权的本地命令，以及 stop、取消文件和复制调用方退出时的清理。大文件测试在 macOS/Linux 采样后台进程 RSS，延迟启动回归让另一端等待 45 秒后再加入连接。所有端到端用例均通过 `start/status/cp/stop` 执行。

此前原型已在 macOS 与 Windows 的不同网络之间双向传过 64 MiB 并核对落盘哈希，成功测试由 macOS 发起连接；Windows 发起的跨网 ICE 失败原因仍未定位。这些历史测量不能替代新版本的完整跨网络验证，也不能保证所有 NAT/防火墙环境都能直连。

2026-11-03 已通过 `0.5.0-rc.1` 线上预发布验证新流程：公网 MCP 启动远端 Windows 组件，返回脚本启动 macOS 组件；两端跨网络建立持久连接，双向复制 1 MiB + 3 字节文件、核对落盘 SHA-256，并确认 stop 收到对端确认。此验证由 macOS 发起协商，不代表 Windows 发起及其他 NAT 环境也已验证。
