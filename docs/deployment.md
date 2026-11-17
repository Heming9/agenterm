# Deployment guide

[简体中文](deployment.zh-CN.md) · [Home](../README.md) · [User guide](usage.md)

Run the server directly on Linux or macOS, or use Docker Compose or systemd on Linux. Target computers can use the macOS, Linux, or Windows client.

## 📋 Before you begin

- Make sure both the agent and target computer can reach the deployment address.
- Download `agenterm-server` for your operating system and architecture from [GitHub Releases](https://github.com/Heming9/agenterm/releases), or build it from source below. Docker Compose builds from source and does not require a separate download.

## 🚀 Option 1: Run directly

On a trusted network, replace `SERVER_IP` with an address reachable by both sides:

```sh
chmod +x agenterm-server
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

On first startup, save the generated management key and configure the MCP URL as described in the [user guide](usage.md#connect-your-agent). This HTTP example works for terminal access. Cross-device P2P requires access to the server over HTTPS for signaling.

## 🐳 Option 2: Docker Compose

Docker and Docker Compose are required. Run:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
install -m 600 .env.example .env
install -d -m 700 .secrets
(umask 077 && openssl rand -hex 24 > .secrets/management-key)
chmod 444 .secrets/management-key
```

Compose mounts the key file at `/run/secrets/management_key`. Mode 0700 on the host directory restricts access to its owner, while mode 0444 on the file allows the container's service user to read the mount. When upgrading, put the existing `AGENTERM_MANAGEMENT_KEY` value from `.env` into the file on one line with the same permissions, then remove the old environment entry. To use another file, set `AGENTERM_MANAGEMENT_KEY_FILE` in `.env`.

Set `AGENTERM_PUBLIC_URL` in `.env` to an address reachable by both sides. The default binding accepts local connections, suitable for a reverse proxy on the host. For direct access on a trusted LAN, set `AGENTERM_BIND_ADDRESS` to `0.0.0.0`.

```sh
docker compose up -d --build
docker compose logs -f server
```

Use the MCP URL displayed in the logs and the key from the management key file to connect your agent. Restarting or recreating the container disconnects devices and clears in-memory sessions.

## ⚙️ Option 3: systemd

This option requires Linux, systemd, and the downloaded Linux server executable. From the repository directory, place the downloaded executable in the current directory as `agenterm-server`, then run:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin agenterm
sudo install -d -m 755 /opt/agenterm
sudo install -m 755 agenterm-server /opt/agenterm/agenterm-server
sudo install -m 644 deploy/systemd/agenterm-server.service /etc/systemd/system/
```

Create the service credential on a new installation:

```sh
sudo sh -c 'umask 077 && openssl rand -hex 24 > /etc/agenterm-management-key'
```

During an upgrade, copy the existing key value from `AGENTERM_MANAGEMENT_KEY` in `/etc/agenterm.env` into the root-owned, mode 0600 file `/etc/agenterm-management-key` as a single line, then remove the key entry from the environment file. This preserves access to an existing runtime. A runtime originally started with `-passwd` retains its original process arguments until it exits. To clear them, arrange a runtime restart after active sessions have ended. The unit uses systemd's `LoadCredential` to provide the file to its service user.

Set the public HTTPS address in `/etc/agenterm.env`:

```text
AGENTERM_PUBLIC_URL=https://YOUR_HOST
```

```sh
sudo chmod 600 /etc/agenterm.env
sudo systemctl daemon-reload
sudo systemctl enable --now agenterm-server
sudo systemctl status agenterm-server
```

The service listens on `127.0.0.1:8080` by default. Make sure the address provided by your deployment environment supports HTTP and WebSocket connections and is reachable by both the agent and target computer. Use the logged `mcp_endpoint` and the key from the credential file to connect your agent.

## ✅ Verify and manage

```sh
# Check health on the server
curl -f http://127.0.0.1:8080/healthz

# Docker Compose logs
docker compose logs -f server

# systemd logs
sudo journalctl -u agenterm-server -f
```

`/healthz` checks that the frontend can reach the runtime. `/livez` checks the runtime itself. Both are root paths.

Compose sends frontend and runtime JSON logs to Docker's rotating local log driver. The systemd unit sends them to journald. When run directly, the frontend writes to stderr and the runtime writes to `runtime.log` in its instance directory by default. Set `AGENTERM_LOG_FORMAT` and `AGENTERM_LOG_LEVEL` to configure format and verbosity. Use `AGENTERM_LOG_FILE` and `AGENTERM_RUNTIME_LOG_FILE` to select log files.

Follow the [user guide](usage.md#-start-a-remote-assistance-session) to authorize a connection from another computer and verify that the agent can run a terminal task.

## 🔨 Build the server from source

Go 1.26.10 or later is required:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
```

After building, deploy using the direct or systemd instructions above.

## Release pipeline

The current workflow builds amd64 and arm64 Rust CLIs for macOS, Linux and Windows, and servers for macOS and Linux. Shared checks and the required CLI and P2P builds start in parallel. Publication requires passing checks, a packaged server and verified downloads. [Release workflow](../.github/workflows/release.yml) · [Component distribution and native caching](../internal/components/assets/README.txt) · [Rust CLI builds](../tools/agenterm-cli/README.md#release-builds)

[RepoCloud](https://repocloud.io/details/AgenTerm/) is a third-party hosting option.

## 📖 Server commands and options

```sh
agenterm-server [start|stop|restart|status] [options]
agenterm-server -help
```

| Command | Description |
| --- | --- |
| `start` | Start or replace the server, retaining the running runtime and sessions. This is the default when no command is given. |
| `stop` | Stop the server and runtime and clear in-memory state. |
| `restart` | Recreate the runtime with the supplied configuration and start the server; existing connections and in-memory state are lost. |
| `status` | Check server and runtime status without starting processes. |

`stop` and `status` accept only `-instance`. The other options apply to `start` and `restart`; changing the startup settings of a running runtime requires `restart`.

If an upgrade reports an incompatible runtime API, follow the diagnostic. Run `agenterm-server stop -instance <name>` with a binary compatible with the old runtime and as the same OS user, then start the new binary. If you no longer have the old binary, use the diagnostic to identify and stop the old runtime. Stopping the runtime disconnects clients and clears in-memory credentials and sessions. Create new connections after the upgrade.

| Option | Description |
| --- | --- |
| `-instance` | Name for an independent server instance; default `default`. |
| `-listen` | Listening address; default `127.0.0.1:8080`. |
| `-public-url` | External address, default `http://127.0.0.1:8080`. This only sets the address; it does not enable HTTPS. Transport encryption is the responsibility of the deployment environment. |
| `-url-prefix` | Path prefix for application routes such as MCP, connections and downloads. The default is `/agenterm`. Use `/` for root routing. Health checks use fixed root paths. |
| `-passwd` | Supply the MCP management key directly. The key is visible in the launching process's command line. |
| `-passwd-file` | Read the MCP management key from a single-line file. Choose one of `-passwd` and `-passwd-file`. With authentication enabled, omitting both generates a key on first startup. |
| `-no-password` | Allow access to the MCP management interface without a password. Mutually exclusive with both key options. |
| `-max-clients-per-ip` | Client connection limit per invitation creator's IP. WebSocket handshakes reserve slots. The default `0` means unlimited. |
| `-client-ip-header` | Defaults to empty and uses the directly connected peer IP. If set, the trusted proxy must overwrite the header with one IP and restrict direct access to the backend. |
| `-enable-short-code` | Enable issuing and redeeming short codes; short codes are disabled by default. |
| `-stdio` | Also serve management MCP over standard input/output, trusting the process owner. |

Short-code lookup failures share a per-IP counter across the browser and client endpoints. MCP administrator key authentication uses an independent counter with the same policy; connection tokens and other authentication interfaces are not rate-limited. After three consecutive failures, requests are rejected for 2 seconds; each subsequent failure adds 2 seconds to the wait, up to 10 minutes. Requests during the wait do not increase the counter. Successful short-code redemption resets the short-code counter; viewing an invitation page does not. Successful administrator authentication resets only the administrator counter. Concurrent requests for each kind of credential share its limiter.
