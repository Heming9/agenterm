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

With the default URL prefix, a server at `https://example.com` serves MCP at `https://example.com/agenterm/mcp`. Set `-url-prefix /` to serve MCP at `/mcp`. A public reverse proxy should terminate TLS and forward HTTP and WebSocket requests with the configured prefix intact.

## 🐳 Option 2: Docker Compose

For a new installation, use Docker, Docker Compose, and OpenSSL:

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

Compose embeds the component bundle from your checkout. If you change the Rust CLI sources or the bundle, run `python3 scripts/build_server.py --prepare-only` before building the image to validate the bundled clients. See [source builds](#-build-the-server-from-source).

```sh
docker compose up -d --build
docker compose logs -f server
```

Use the MCP URL displayed in the logs and the key from the management key file to connect your agent. Restarting or recreating the container disconnects devices and clears in-memory sessions.

## ⚙️ Option 3: systemd

This option requires Linux, systemd, and the downloaded Linux server executable. From the repository directory, place the downloaded executable in the current directory as `agenterm-server`, then run:

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin agenterm
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

`/healthz` checks both the server frontend and the runtime. `/livez` checks the runtime listener. For a direct launch, the frontend logs to stderr by default and the runtime writes `runtime.log` in its instance directory. The supplied Compose and systemd configurations collect logs from both processes. Use `AGENTERM_LOG_FORMAT` (`text` or `json`) and `AGENTERM_LOG_LEVEL` (`DEBUG`, `INFO`, `WARN` or `ERROR`) to configure them.

After connecting your agent, create an invitation, authorize the connection from another computer, and verify that the agent can perform a terminal task. To issue browser invitation links for single-device invitations, include `-enable-short-code` in the runtime startup options.

## 🔨 Build the server from source

Use Go 1.26.10 or later and Python 3.11 or later. The build script verifies the embedded client versions, source fingerprint, and executable hashes before compiling the server. If validation fails, [rebuild the Rust CLI bundle](../components/agenterm-cli/README.md#release-builds).

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
python3 scripts/build_server.py --output agenterm-server
```

After building, deploy using the direct or systemd instructions above.

## Release pipeline

The [release workflow](../.github/workflows/release.yml) runs [build checks](../.github/workflows/checks.yml), six native Rust CLI builds, and native P2P builds in parallel. Server packaging waits for CLI and P2P distributions; publication requires all checks and generated client download validation to pass. The release includes CLI binaries for macOS, Linux and Windows and Server binaries for macOS and Linux, each for amd64 and arm64. Selective releases never reuse legacy Go CLI or GUI assets.

See [Rust CLI release builds](../components/agenterm-cli/README.md#release-builds) to rebuild the client bundle. Linux CLI downloads use system OpenSSL and are built on Ubuntu 24.04; target machines need compatible glibc and OpenSSL shared libraries.
Native P2P caches require an exact fingerprint of the component source, native validation scripts, packaging workflow, Rust version, runner and target. Restored artifacts are checked for component version, executable architecture and SHA-256 before use. Missing or invalid caches trigger a full native build; Cargo dependencies and compilation outputs have a separate cache.

The native cache is saved after Rust and native end-to-end checks pass, so later Go integration failures do not force another Rust build. Server signaling and bootstrap script tests always run, including on native cache hits. The standalone P2P workflow uses the same early checks; a release skips that duplicate gate only after its own shared checks pass. Older source refs without the native cache helper retain the full build path. The first run remains a cold build; warm-cache runtime must be measured in Actions.

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

### When an upgrade reports a runtime API mismatch

The current runtime uses API 14. If the new binary reports an incompatible runtime, stop the old runtime with the server executable that launched it. Run `/path/to/old/agenterm-server stop -instance <name>` as the same OS user and with the same instance name. If that executable is unavailable, follow the compatibility diagnostic to identify and stop the old runtime. Then start the new server with the desired configuration. Stopping the runtime disconnects clients and clears in-memory credentials and sessions. Create new invitations after the upgrade.

### Startup options

| Option | Description |
| --- | --- |
| `-instance` | Name for an independent server instance; default `default`. |
| `-listen` | Listening address; default `127.0.0.1:8080`. |
| `-public-url` | External address, default `http://127.0.0.1:8080`. This only sets the address; it does not enable HTTPS. Transport encryption is the responsibility of the deployment environment. |
| `-url-prefix` | URL path prefix for all endpoints; default `/agenterm`. Use `/` for root routes. |
| `-passwd` | Supply the MCP management key directly. The key is visible in the launching process's command line. |
| `-passwd-file` | Read the MCP management key from a single-line file. Choose one of `-passwd` and `-passwd-file`. With authentication enabled, omitting both generates a key on first startup. |
| `-no-password` | Allow access to the MCP management interface without a password. Mutually exclusive with both key options. |
| `-max-clients-per-ip` | Maximum connected clients per invitation creator's IP; default `0`, meaning unlimited. |
| `-client-ip-header` | Trusted request header containing a single client IP; empty by default, using the directly connected peer IP. |
| `-enable-short-code` | Enable issuing and redeeming short codes; short codes are disabled by default. |
| `-stdio` | Also serve management MCP over standard input/output, trusting the process owner. |

Short-code lookup failures share a per-IP counter across the browser and client endpoints. MCP administrator key authentication uses an independent counter with the same policy; connection tokens and other authentication interfaces are not rate-limited. After three consecutive failures, requests are rejected for 2 seconds; each subsequent failure adds 2 seconds to the wait, up to 10 minutes. Requests during the wait do not increase the counter. Successful short-code redemption resets the short-code counter; viewing an invitation page does not. Successful administrator authentication resets only the administrator counter. Concurrent requests for each kind of credential share its limiter.
