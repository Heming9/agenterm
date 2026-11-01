# Deployment guide

[简体中文](deployment.zh-CN.md) · [Home](../README.md) · [User guide](usage.md)

## Start the server

Download the server for your system from [Releases](https://github.com/Heming9/agenterm/releases). This example is for a trusted local network:

```sh
./agenterm-server -listen 0.0.0.0:8080 -public-url http://SERVER_IP:8080
```

Replace `SERVER_IP` with an address reachable by both your agent and target computers. The server prints its MCP address and management key. Add them to your agent's remote MCP configuration:

```text
URL:            http://SERVER_IP:8080/agenterm/mcp
Authorization:  Bearer MANAGEMENT_KEY_PRINTED_BY_SERVER
```

Then follow the [invitation and authorization steps](../README.md#quick-start) to connect a target computer. For public deployments, configure an HTTPS reverse proxy with WebSocket support. `-public-url` declares the public address; it does not enable HTTPS itself.

If you already have a connection token, start the CLI or GUI client and paste it:

```sh
./agenterm-client
```

Use server and client binaries from the same release. On Windows, run the corresponding `.exe` files.

## Run with Docker

The container runs only the server. Clients still run on the computers you want to assist; no host directories or Docker socket need to be mounted. The image includes the one-command installers and all six CLI downloads, just like the standalone server.

With Docker and the Compose plugin installed, run from the repository root:

```sh
cp .env.example .env
openssl rand -hex 24
```

Edit `.env`: put the generated value in `AGENTERM_MANAGEMENT_KEY` and set `AGENTERM_PUBLIC_URL` to an address reachable by both sides. The default `http://localhost:8080` works only when both sides use the Docker host. Then start the service:

```sh
docker compose up -d --build
docker compose logs -f server
```

Configure your agent with `AGENTERM_PUBLIC_URL` followed by `/agenterm/mcp` and `Authorization: Bearer YOUR_MANAGEMENT_KEY`. Keep `.env` private; it is excluded from Git and the image build context. Compose requires a nonempty key, so recreating the container keeps the same management credentials.

Compose publishes port 8080 on `127.0.0.1` by default. For access from a trusted LAN, set `AGENTERM_BIND_ADDRESS=0.0.0.0` and use the Docker host's LAN IP in `AGENTERM_PUBLIC_URL`. For public access, use an HTTPS reverse proxy with WebSocket support and set the public URL accordingly. A proxy running in another container must share a Docker network and forward to `server:8080`; `127.0.0.1` inside that proxy container refers to the proxy itself. Preserve the `/agenterm` path when forwarding.

To build and run without Compose:

```sh
docker build -t agenterm-server:local .
docker run -d --name agenterm-server --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -p 127.0.0.1:8080:8080 agenterm-server:local \
  -listen 0.0.0.0:8080 -public-url http://localhost:8080
docker logs agenterm-server
```

This form generates and prints a new management key on each restart. Append `-passwd YOUR_MANAGEMENT_KEY` to keep a fixed key, or use the Compose configuration above. Other [startup options](#startup-options) can also be passed as container arguments. When overriding arguments, retain `-listen 0.0.0.0:8080` so Docker can forward requests to the server.

The image uses a [multi-stage build](https://docs.docker.com/build/building/multi-stage/), a static Go binary, and an unprivileged runtime user. It needs no writable filesystem or data volume. Invitations, client credentials, and sessions are held in memory: restarting the server clears them, so create new invitations and reconnect clients afterward. Run a single server instance; independent replicas do not share connection state.

Build Linux amd64 and arm64 images with Buildx (replace the example registry path with your own):

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -t YOUR_REGISTRY/agenterm-server:YOUR_TAG --push .
```

For an update, pull the desired source revision and run `docker compose up -d --build` again. Stop the service with `docker compose down`.

## Use a stdio MCP client

For agents that launch a local MCP process, add a configuration like this (replace the executable path and `SERVER_IP`):

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

Stdio grants management access to the user who launches the process; it needs no bearer header. HTTP management authentication remains enabled. The HTTP listener still serves invitations, installers, and target-client WebSockets, so target computers must be able to reach the configured public URL. Use HTTPS through a reverse proxy for public deployments, as described above.

In this mode, stdout contains only MCP messages and startup information goes to stderr. Closing the stdio connection stops the HTTP server too. The interactive Enter-to-regenerate-invitation shortcut is available only in HTTP-only mode.

## Startup options

View available startup options:

```sh
./agenterm-server -h
./agenterm-client -h
```

- Set a fixed management key with `--passwd` to keep your agent configuration across server restarts.
- Customize the deployment path with `-url-prefix`, updating the agent and reverse proxy configuration to match.
- Limit online devices per invitation creator's IP with `--max-clients-per-ip`.
- Behind a reverse proxy, use `--client-ip-header` if you need the visitor's IP. Restrict server access to trusted proxies that overwrite that header.

## Build from source

Requires Go 1.26.10+:

```sh
git clone https://github.com/Heming9/agenterm.git
cd agenterm
go build -o agenterm-server ./cmd/agenterm-server
go build -o agenterm-client ./cmd/agenterm-client
```

The desktop client also needs CGo and a C compiler. Linux requires graphics development libraries; see [Fyne setup](https://docs.fyne.io/started/).

```sh
go build -o agenterm-gui ./cmd/agenterm-gui
```

Ordinary server builds embed the checked-in CLI bundle for macOS, Linux, and Windows (amd64/arm64) and provide both one-command installers by default. Download URLs use `internal/version.Current`; no release linker flag is needed. The server binary contains everything needed to serve installers, client archives, and checksums.

After changing CLI or shared client code, regenerate and include the updated bundle with the source changes:

```sh
go generate ./internal/installer
go build -o agenterm-server ./cmd/agenterm-server
```

Regeneration requires Python 3 and Go, cross-compiles all six CLI targets, and atomically replaces `internal/installer/assets/clients.tar.xz`. Ordinary builds and running the server do not require Python, XZ, or a Go installation on the server machine. A missing bundle fails compilation instead of silently disabling installer commands.
