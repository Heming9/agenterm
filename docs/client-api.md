# Terminal client API

The Rust CLI executes terminal commands and owns session state, processes, output and retention. The Go server uses [the shared terminal request and response types](../internal/terminalapi/types.go) to expose this API over MCP. See the [Rust CLI](../components/agenterm-cli/README.md) and [MCP tool reference](mcp-tools.md) for the public interfaces.

`protocol.Message` carries `api_call`, `api_cancel` and `api_result` envelopes with JSON request and result bodies. The server and client negotiate the terminal API version in the WebSocket handshake. Cancelling an API request cancels the response waiter. Commands that the client has accepted continue running.

`internal/service` authenticates callers, routes terminal requests to connected devices and translates their responses for MCP. `session_list` queries the selected clients and attaches their device IDs. MCP supports status filtering and limits command previews to 160 characters, including the `... (truncated)` suffix.

A session ID identifies a terminal on one device. When multiple devices are connected, use `client_id` and `session_id` together. Separate devices can use the same session name.

After a temporary network failure, reconnecting the same client process exposes the same sessions and cached results without replaying commands. Explicit client shutdown or permanent rejection closes its terminals. While the client is connected, up to ten closed session records remain readable for up to 30 minutes.

New command IDs use eight case-sensitive alphanumeric characters. Older command IDs remain accepted.
