# Deployment guide

[简体中文](deployment.zh-CN.md) · [Home](../README.md) · [User guide](usage.md)

## Start Server

Build or download **one `agenterm-server` executable**. Existing startup flags remain accepted:

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

Server automatically starts or adopts its internal Runtime. Runtime has no standalone executable, public startup command, container or service; Server owns its creation, adoption, shutdown and recreation.

Runtime owns the public TCP listener, client WebSockets, authentication, invitations, devices and session state. It forwards other HTTP requests to Server, which implements MCP, installers and downloads over private Unix sockets.

On first startup, Server prints the MCP URL and generates a management key if `-passwd` is omitted. Restarting Server reuses the existing Runtime key:

```text
URL:            http://SERVER_IP:8080/agenterm/mcp
Authorization:  Bearer KEY_FROM_FIRST_STARTUP
```

Both processes run as the same user. Default sockets live under the system temporary directory in `agenterm-<uid>/`, with directory mode `0700` and socket mode `0600`. Internal paths are automatically managed through `-instance`; no socket flags are needed.

All credentials, invitations, output and sessions stay in Runtime memory. Socket files are IPC endpoints, not saved business state. For public access, keep an HTTPS reverse proxy with WebSocket support forwarding to port 8080 and preserving `/agenterm`. `-public-url` does not enable TLS. Hosting currently requires Linux/macOS; Windows target clients remain supported.

## Unified component bundle

Client CLI and P2P platform downloads are merged into `internal/components/assets/components.tar.xz`. Ordinary Go builds embed this unified archive. Build the Server executable with:

```sh
python3 scripts/build_server.py --output dist/agenterm-server --goos linux --goarch amd64
```

Deploy only the Server executable. Startup verifies tar.xz paths, sizes and SHA-256, then atomically extracts into `components/<bundle-hash>/` under the instance runtime directory. CLI and P2P downloads both come from there; no external `p2p-assets` directory is required. Valid caches are verified and reused, and different bundle hashes preserve old frontends’ assets. This writable runtime directory supports systemd PrivateTmp and read-only Docker root filesystems. Startup never recompresses downloads and needs no Python, Rust or system decompression utility.

## Commands and options

```sh
agenterm-server [start|stop|restart|status] [options]
```

Omitting the command selects `start`. The public CLI has nine options:

| Option | Default | Purpose |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8080` | Public TCP listener |
| `-public-url` | `http://127.0.0.1:8080` | Address reachable by both sides |
| `-url-prefix` | `/agenterm` | Branding and deployment prefix; `/` selects root routes |
| `-passwd` | Generated when Runtime is first created | MCP management key |
| `-no-password` | `false` | Explicitly allow password-free management |
| `-stdio` | `false` | Also serve stdio MCP |
| `-max-clients-per-ip` | `0`, unlimited | Online device quota per invitation creator IP |
| `-client-ip-header` | Empty | Trusted proxy header containing one source IP; empty uses the direct peer |
| `-instance` | `default` | Separate independent instances owned by the same user |

Instance names contain 1–40 lowercase letters, digits, hyphens or underscores, starting with a letter or digit. Independent instances need different listening ports and matching public URLs. `stop` and `status` accept only `-instance`.

| Operation | Behavior |
| --- | --- |
| `agenterm-server` or `agenterm-server start` | Create or adopt Runtime; automatically publish a ready replacement Server and drain its predecessor |
| Ctrl+C / SIGTERM | Drain Server requests for up to 70 seconds, keeping Runtime for adoption |
| `agenterm-server status` | Check without creating directories or starting processes; exit 0 only when both components are ready |
| `agenterm-server stop` | Fully stop the instance's Server and Runtime and clear memory state |
| `agenterm-server restart` with the complete startup configuration | Fully stop, recreate Runtime and start Server; connections and sessions are cleared |

Runtime retains its configuration. Omitted Runtime options do not override existing values on adoption; conflicting explicit settings fail. Use `restart` with the complete desired configuration to change settings. Generated management keys are also retained with Runtime.

```sh
# Initial startup, or replace Server while adopting the existing Runtime.
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080 -passwd YOUR_KEY

./agenterm-server status
./agenterm-server stop

# Explicit Runtime reconfiguration clears credentials, connections and sessions.
./agenterm-server restart -listen 0.0.0.0:8080 \
  -public-url http://SERVER_IP:8080 -passwd YOUR_KEY -max-clients-per-ip 8

# Run and manage an isolated staging instance.
./agenterm-server -instance staging -listen 127.0.0.1:8081 -public-url http://localhost:8081
./agenterm-server status -instance staging
./agenterm-server stop -instance staging
```

Runtime detaches from the terminal process group and closes its startup pipe after reporting readiness. It survives Server exits and crashes, with no automatic orphan timeout, until explicitly stopped through Server or the machine restarts.

A replacement Server becomes ready on its own temporary socket, automatically switches the internal endpoint and asks its predecessor to drain. No socket flags or manual switching are needed. Running Runtime code is not replaced; use `restart` when it must be upgraded. While no Server is running, Runtime keeps buffering session output and new MCP requests receive 503 until Server returns.

Server checks the private API version before taking over or stopping Runtime. Release versions may differ when their API versions match. An incompatible Server exits with a nonzero status and reports both release/API versions, the Runtime PID and recovery commands. It does not restart Runtime or switch the existing Server. This also applies to `stop`, `restart` and `status` from that incompatible executable.

Use a compatible Server executable to stop the same instance, for example `/path/to/compatible/agenterm-server stop -instance staging`, then start the new version. Alternatively, run the reported `kill -TERM <PID>` as the same user on the same host or inside the same container. Stopping Runtime disconnects clients and clears in-memory credentials and sessions. Old runtimes that do not report a release version or PID show those fields as unknown; verify the PID before sending a signal.

Migrating from the old single-process architecture requires reconnecting clients once. Interactive sessions resume through their original ID, cursor and `session_list`. Synchronous commands survive a Server crash, but interrupted MCP responses cannot be recovered automatically; check task state before replaying commands.

Migration from previous options: `-stop` becomes `stop`, `-restart-runtime` becomes `restart`, and `-check` becomes `status`. The three socket flags become `-instance`; draining is fixed at 70 seconds. `-generate` is removed; create invitations through MCP `create_client`. If an older instance used custom socket paths, fully stop it with the older executable before adopting the instance naming scheme.

## Docker

Compose declares one Server service, and Server manages Runtime inside that container. No standalone Runtime image or container is shipped. The unprivileged container uses a tmpfs `/tmp` for sockets; business state stays in memory without host directory or Docker socket mounts.

```sh
cp .env.example .env
openssl rand -hex 24
```

Set `AGENTERM_MANAGEMENT_KEY` and a reachable `AGENTERM_PUBLIC_URL` in `.env`:

```sh
docker compose up -d --build
docker compose logs -f server
```

Port 8080 binds to `127.0.0.1` by default. Use `AGENTERM_BIND_ADDRESS=0.0.0.0` for trusted LAN access. An HTTPS proxy in another container forwards to `server:8080`.

**Restarting or recreating the container also kills Runtime and loses connections and memory state.** Docker Compose restart/up/down operations manage the entire container, unlike replacing only the host Server process. Use native/systemd hosting below when connections must survive Server updates.

## systemd

Only the [Server unit](../deploy/systemd/agenterm-server.service) is needed. Create an `agenterm` user, install the Server binary in `/opt/agenterm/`, install the unit in `/etc/systemd/system/`, and create an administrator-readable `/etc/agenterm.env`:

```text
AGENTERM_PUBLIC_URL=https://YOUR_HOST
AGENTERM_MANAGEMENT_KEY=YOUR_FIXED_KEY
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now agenterm-server
```

Atomically replace the Server executable and run `sudo systemctl restart agenterm-server`. The unit uses `KillMode=process` to stop only the main Server while leaving Runtime for its replacement.

For a full shutdown, use Server's management command:

```sh
sudo systemctl stop agenterm-server
sudo -u agenterm /opt/agenterm/agenterm-server stop
```

The temporary directory contains IPC sockets, the backend link and, by default, Runtime operational logs; credentials and terminal output remain in memory. For Runtime configuration changes, fully stop services as above, update the environment and start the service again.

## Stdio MCP

An agent still launches one executable, which starts or adopts Runtime automatically:

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

Stdio grants management access to the local process owner; HTTP management still requires authentication. Stdout contains MCP messages only; startup output and generated keys use stderr. Closing stdio exits Server but leaves Runtime for the next launch. A real listening socket can only be used by one Server at a time.

## Logging and production troubleshooting

Server and Runtime use Go's standard `log/slog`. Records include `time`, `level`, `msg`, `component`, `version`, `pid` and `instance`. Configure logging with environment variables; the public CLI is unchanged:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENTERM_LOG_FORMAT` | `text` | Local readable `text`, or production `json` |
| `AGENTERM_LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |
| `AGENTERM_LOG_FILE` | Empty, Server stderr | Absolute Server log file path; create its parent directory first |
| `AGENTERM_RUNTIME_LOG_FILE` | `runtime.log` in the instance directory | Absolute independent Runtime log path, or explicit `stderr` for a platform collector |

The default Runtime file is `agenterm-<uid>/runtime.log` under the system temporary directory, or `agenterm-<uid>/<instance>/runtime.log` for a named instance. Long temporary paths fall back to `/tmp` for IPC. Files are append-only with mode `0600` and contain no saved business state. System cleanup can remove temporary logs; use platform collection or a persistent directory for retention. File output has no automatic rotation: configure an operations tool such as logrotate with `copytruncate` and a retention limit. Renaming alone leaves Runtime writing the original open file.

Runtime does not retain the launching terminal or MCP output pipe by default. Its independent sink continues across Server exits, crashes and updates. Compose sends both components as JSON to container logs, using the `local` driver with 10 MiB files and a five-file limit. The systemd unit sends JSON to journald; configure retention in the host's journald settings:

```sh
docker compose logs --since 15m -f server
sudo journalctl -u agenterm-server --since '15 minutes ago' -f -o cat
```

Use `stderr` only with collectors whose lifetime is independent of Server, such as Docker or journald. Keep the independent file default for stdio MCP so Runtime does not retain the launcher's stderr pipe. Replacing Server retains Runtime's logging configuration. Changing Runtime's level, format or destination requires an explicit `restart`, which disconnects devices and clears sessions.

Public HTTP requests get a fresh `X-Request-ID`, replacing any caller-supplied value and echoing the generated ID in the response. The ID follows Server, private Runtime RPCs, MCP tools and command/session lifecycle logs. Stdio tool calls also get a correlation ID. Search by `request_id`, then follow `client_id`, `command_id` or `session_id` for longer device/task lifecycles. Command IDs match the execution message IDs sent to clients. Successful health checks and private RPCs are DEBUG; failed requests remain WARN/ERROR.

Logs include status, duration, exit code, timeout/cancellation flags, WebSocket close code and error type. Proxy connection failures also include the underlying error. HTTP panics retain a stack without the panic value. Routes use templates such as `/c/{code}`: installation codes, raw URLs, queries, authorization headers, command bodies, environment values, session input and terminal output are omitted. Sensitive named attributes have a redaction backstop; never interpolate credentials into messages or arbitrary error strings. Automatically generated management keys still appear in the initial startup instructions; production deployments should supply a fixed key as the Compose/systemd examples do.

Start troubleshooting with `status` and the two health endpoints:

- `/livez` fails: inspect Runtime startup, termination and host resources.
- `/livez` works but `/healthz` fails: use the response's `X-Request-ID` to find `server proxy failed` or `runtime health check failed`, then inspect Server, private sockets and API compatibility.
- Device disconnects: find `client disconnected`, `client heartbeat failed` or `client output queue full`, then follow `client_id` to command/session events.
- Commands fail or time out: inspect `command completed` / `session completed`, especially `exit_code`, `timed_out` and `canceled`, then inspect client-visible output.

This supplies diagnostic logging. Centralized log storage, external probes and alert delivery are not configured in this repository.

## Health checks

```sh
./agenterm-server -h
./agenterm-server status
```

`GET /livez` checks Runtime; `GET /healthz` checks Server and the private API. Server never implicitly replaces an incompatible Runtime. Use `-client-ip-header` only behind a trusted proxy that overwrites the selected header and restricts direct access.

## Build from source

Requires Go 1.26.10+:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client-cli ./cmd/agenterm-client-cli
```

The desktop client also needs CGo and a C compiler. Linux requires graphics development libraries; see [Fyne setup](https://docs.fyne.io/started/).

```sh
go build -o agenterm-client-gui ./cmd/agenterm-client-gui
```

The CLI and GUI have separate entry packages and share connection and startup logic in `internal/client`. Release assets use `agenterm-client-cli-<platform>-<arch>` and `agenterm-client-gui-<platform>-<arch>` prefixes. When a selective release uses a base with the older asset names, the planner rebuilds all programs to migrate the complete release together.

Ordinary server builds embed `components.tar.xz`, containing raw Client CLI and P2P CLI executables for macOS/Linux/Windows (amd64/arm64), and provide both one-command installers by default. Startup only extracts and verifies the original executables. Download requests generate the selected archive format on demand and cache its bytes. Installers probe available decompression tools and select XZ, a compatible gzip/ZIP format, or a direct executable download; all paths verify the executable SHA-256. Download URLs use `internal/version.Current`; no release linker flag is needed.

After changing CLI or shared client code, regenerate and include the updated bundle with the source changes:

```sh
go generate ./internal/components
go build -o agenterm-server ./cmd/agenterm-server
```

Regeneration requires Python 3.11+ and Go, cross-compiles six CLI targets, reuses the existing bundle’s raw P2P executables, and atomically replaces `internal/components/assets/components.tar.xz`. To update P2P, first run `scripts/build_p2p_cli.py` for all six OS/architecture combinations, then merge with `python3 scripts/build_components.py --p2p-assets <native-output-directories...>`. Missing P2P targets fail packing. Release CI joins six native P2P builds, the unified bundle and Server embedding. A missing bundle fails compilation instead of silently disabling component downloads.

GitHub Release attachments contain fourteen original executables (six CLI, six Server, and the macOS/Windows GUI programs) plus `release-manifest.json`. Component bundles and per-platform archives are not attached. On macOS/Linux, run `chmod +x <downloaded-program>` before launching a directly downloaded executable. The macOS GUI attachment is the program from inside the app bundle.
