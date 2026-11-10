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

On first startup, the terminal displays the MCP URL and management key. Save the key securely and connect the MCP URL to your agent as described in the [user guide](usage.md#connect-your-agent).

## 🐳 Option 2: Docker Compose

Docker and Docker Compose are required. Run:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
cp .env.example .env
openssl rand -hex 24
```

Edit `.env`: set `AGENTERM_MANAGEMENT_KEY` to the generated value and `AGENTERM_PUBLIC_URL` to an address reachable by both sides. The default binding accepts local connections, suitable for a reverse proxy on the host. For direct access on a trusted LAN, set `AGENTERM_BIND_ADDRESS` to `0.0.0.0`.

```sh
docker compose up -d --build
docker compose logs -f server
```

Use the MCP URL displayed in the logs and the management key from `.env` to connect your agent. Restarting or recreating the container disconnects devices and clears in-memory sessions.

## ⚙️ Option 3: systemd

This option requires Linux, systemd, and the downloaded Linux server executable. From the repository directory, place the downloaded executable in the current directory as `agenterm-server`, then run:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin agenterm
sudo install -d -m 755 /opt/agenterm
sudo install -m 755 agenterm-server /opt/agenterm/agenterm-server
sudo install -m 644 deploy/systemd/agenterm-server.service /etc/systemd/system/
```

Generate a management key:

```sh
openssl rand -hex 24
```

Create `/etc/agenterm.env` with your public HTTPS address and the generated key:

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

The service listens on `127.0.0.1:8080` by default. Make sure the address provided by your deployment environment supports HTTP and WebSocket connections and is reachable by both the agent and target computer. Use the configured address and key to connect your agent.

## ✅ Verify and manage

```sh
# Check health on the server
curl -f http://127.0.0.1:8080/healthz

# Docker Compose logs
docker compose logs -f server

# systemd logs
sudo journalctl -u agenterm-server -f
```

After connecting your agent, create an invitation, run its connection command on another computer, and authorize access. Confirm that the agent can perform a terminal task. Share management keys and invitation commands only with people you trust.

## 🔨 Build the server from source

Go 1.26.10 or later is required:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
```

After building, deploy using the direct or systemd instructions above.

## Release pipeline

The [release workflow](../.github/workflows/release.yml) first runs the shared Linux and Windows [build checks](../.github/workflows/checks.yml). GUI and native P2P builds start after both pass. Server packaging waits for the P2P distribution; generated client downloads are still checked before publication.

Native P2P caches require an exact fingerprint of the component source, native validation scripts, packaging workflow, Rust version, runner and target. Restored artifacts are checked for component version, executable architecture and SHA-256 before use. Missing or invalid caches trigger a full native build; Cargo dependencies and compilation outputs have a separate cache.

The native cache is saved after Rust and native end-to-end checks pass, so later Go integration failures do not force another Rust build. Current Go bootstrap tests always run, including on native cache hits. The standalone P2P workflow uses the same early checks; a release skips that duplicate gate only after its own shared checks pass. Older source refs without the native cache helper retain the full build path. The first run remains a cold build; warm-cache runtime must be measured in Actions.

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

**Upgrading runtime API 12 to 13:** The old runtime is incompatible with the new short-code and authentication behavior. Stop it with its existing API 12 server executable before starting the API 13 server. If it is unavailable, follow the compatibility diagnostic to identify and stop the old runtime. Stopping the runtime disconnects clients and clears in-memory credentials and sessions; recreate connections after upgrading.

**Upgrading runtime API 7 to 8:** Keep the existing API 7 server executable until the old runtime has been stopped. Run `/path/to/api7/agenterm-server stop -instance <name>` using the same user and instance, then start the API 8 server with the intended configuration. API 8 `start`, `stop`, and `restart` cannot manage an API 7 runtime; if the previous executable is unavailable, follow the compatibility diagnostic to identify and stop the old runtime. Stopping the runtime disconnects clients and discards in-memory credentials and sessions; create new connections after the upgrade. Never downgrade authentication or send control credentials to an older runtime to bypass this check.

| Option | Description |
| --- | --- |
| `-instance` | Name for an independent server instance; default `default`. |
| `-listen` | Listening address; default `127.0.0.1:8080`. |
| `-public-url` | External address, default `http://127.0.0.1:8080`. This only sets the address; it does not enable HTTPS. Transport encryption is the responsibility of the deployment environment. |
| `-url-prefix` | URL path prefix for all endpoints; default `/agenterm`. Use `/` for root routes. |
| `-passwd` | MCP management key; generated when omitted. Cannot be combined with `-no-password`. |
| `-no-password` | Allow access to the MCP management interface without a password. |
| `-max-clients-per-ip` | Maximum connected clients per invitation creator's IP; default `0`, meaning unlimited. |
| `-client-ip-header` | Trusted request header containing a single client IP; empty by default, using the directly connected peer IP. |
| `-enable-short-code` | Enable issuing and redeeming short codes; short codes are disabled by default. |
| `-stdio` | Also serve management MCP over standard input/output, trusting the process owner. |

Short-code lookup failures share a per-IP counter across the browser and client endpoints. MCP administrator key authentication uses an independent counter with the same policy; connection tokens and other authentication interfaces are not rate-limited. After three consecutive failures, requests are rejected for 2 seconds; each subsequent failure adds 2 seconds to the wait, up to 10 minutes. Requests during the wait do not increase the counter. Successful short-code redemption resets the short-code counter; viewing a short-link page does not. Successful administrator authentication resets only the administrator counter. Concurrent requests for each kind of credential share its limiter.
