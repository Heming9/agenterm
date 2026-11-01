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
