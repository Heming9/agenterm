# p2p-cli

Rust 实现的独立 P2P 文件传输组件。`start` 建立并维护一条后台连接，`status` 查看连接，`cp` 在同一连接上双向复制文件，`stop` 通知双方断开。文件只走加密直连，不回退到服务器中转。

## 通过 Agenterm 初始化

使用支持 P2P 的服务端和 Rust CLI。Agent 与目标设备需能直连，跨设备信令使用 HTTPS。

1. Agent 调用 `p2p_transfer_start`。多设备令牌需要指定 `client_id`。管理访问还需要传入 `control_token`，详见 [MCP 访问方式](../../docs/mcp-tools.zh-CN.md#访问方式)。
2. Rust CLI 执行服务端生成的脚本，下载、校验并以前台受控进程运行远端 `p2p-cli`。连接断开时，客户端会停止该进程。
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
- `status --verbose`（或 `-v`）提供完整诊断信息。失败原因在进程退出后仍可查询。
- `cp` 只支持本机与对端之间的单个普通文件。使用 MCP 或 `status` 返回的端点 ID。建议使用绝对路径，含空格时对整个操作数加引号。Windows 可用 `client-ID:C:\Users\name\file.txt`；相对路径以对应后台进程的工作目录为准。
- 默认拒绝覆盖。`--overwrite` 会先写入并校验临时文件，然后原子替换，原文件保留到提交成功。目录和目标符号链接拒绝写入。
- 同一连接一次复制一个文件，可以连续、双向执行多次 cp。完成结果包含字节数、SHA-256 和 `receiver_committed`。
- `stop` 取消当前复制，清理未提交临时文件，并通知对端关闭连接。返回的 `peer_confirmed` 表示是否收到对端确认；对端无法到达时不声称确认成功。已经提交的文件保留。
- 初始化脚本结束不影响 Agent 端后台连接；Client 退出或断开时，先请求其受控组件停止，再回收进程树。异常强杀或掉电仍可能留下临时文件；对端通过保活检测断连。

### 状态字段

| 字段 | 含义 |
| --- | --- |
| `state`、`peer.state` | 本机和对端的连接状态。 |
| `path` | ICE 选中的 UDP 地址及路径类型，未建立连接或未取得路径时为 `null`。`same_subnet` 按本机接口掩码判定，VPN 也可能命中。其他类型为 `loopback`、`direct`。 |
| `copy` | 当前复制的已处理字节数、总字节数和实时速率（B/s）。空闲时为 `null`。 |
| `--verbose` 额外字段 | 连接标识、程序路径、工作目录、最近通信时间、系统和架构，以及本次复制的平均速率等详细信息。 |

## 协商参数与协议

组件仅依赖协商入口和 `v1` 协议。最小配置如下，ID 与凭据由 `p2p_transfer_start` 产生：

```json
{
  "signal_url": "https://server.example/agenterm/p2p",
  "session": "32个十六进制字符的连接标识",
  "token": "该端点的私有十六进制接入凭据"
}
```

组件向 `/v1/{session}/register` 注册，从响应获取端点 ID、协商角色和 STUN 配置。显示 ID 用于指定复制端点，配对凭据用于鉴权。两端凭据不同；只有指定的一端能写 offer，另一端能写 answer。SDP 写入幂等、不可覆盖，每条上限 128 KiB。协商角色与文件方向独立。

只接受 HTTPS，测试时仅对回环地址允许 HTTP，不跟随重定向。ICE 对本机地址的优先级高于 STUN 公网映射地址；同一局域网的地址能够互通时优先使用本机地址，最终选择仍取决于连通性和 ICE 协商。连接建立后文件复制和保活直接走 DataChannel；Server 不参与文件数据传输。当前支持 IPv4，候选收集最多等待约五秒；IPv6、目录复制、断点续传和网络切换后自动重连尚未实现。

可选配置包括 `timeout_seconds`（建连及每次复制的期限，独立运行默认 600 秒，Agenterm 生成的配置为 300 秒，允许范围为 1–3600 秒）、`max_bytes`（默认 10 GiB）、`cancel_file` 和 `bind_ip`。测试夹具可显式设置 `register: false` 并提供 `client_id`、`offerer` 和 `stun`，直接测试 SDP 邮箱；正常 Agenterm 初始化不需要这些测试参数。

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

收齐原生 P2P 产物后，按[组件分发说明](../../internal/components/assets/README.txt)与 CLI 一起打包并构建服务端。

`P2P_CLI_STUN` 在 Runtime 启动时配置 STUN 主机与端口，默认 `stun.l.google.com:19302`，本机测试设为 `none`。它由协商协议返回给组件，组件使用者无需另行配置；更改已有 Runtime 的环境配置需要按既有规则重启 Runtime。

[P2P 工作流](../../.github/workflows/p2p-cli.yml) 在六种原生平台上验证组件和服务端协议，并合并分发产物。

## 验证

```sh
cargo test --manifest-path tools/p2p-cli/Cargo.toml --locked
cargo clippy --manifest-path tools/p2p-cli/Cargo.toml --all-targets --locked -- -D warnings
cargo build --release --locked --manifest-path tools/p2p-cli/Cargo.toml
python3 tools/p2p-cli/daemon_e2e.py tools/p2p-cli/target/release/p2p-cli
# 单独运行 45 秒延迟启动回归
python3 tools/p2p-cli/daemon_e2e.py tools/p2p-cli/target/release/p2p-cli --delayed-only
```

服务端信令和启动脚本测试：

```sh
go test ./internal/p2p ./internal/service -run 'TestP2P|TestManifest' -count=1
```

设置 `AGENTERM_RUST_CLI` 与 `AGENTERM_GO_SERVER` 指向已构建的二进制后，运行
`go test ./tools/agenterm-cli/tests -run 'P2PBootstrapLifecycle|RustCLIRealServer' -count=1`
可验证协议回执、停止和重连清理。在 macOS 和 Linux 上也会通过真实 MCP 启动组件并双向传输文件。

Windows 上将端到端测试中的程序路径改为相应的 `.exe`。直连能否建立取决于 NAT 与防火墙。

历史跨网验证见[预发布记录](../../deploy/staging/README.md)。
