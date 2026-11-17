# Terminal client API

The [Rust CLI](../tools/agenterm-cli/README.md) executes terminal commands and owns session state, processes, output and retention. The Go server uses the request and response types in `internal/terminalapi` to expose the terminal API through MCP.

`protocol.Message` carries `api_call`, `api_cancel` and `api_result` envelopes with JSON request and result bodies. The server and client negotiate the terminal API version in the WebSocket handshake. Cancelling an API request cancels the response waiter. Commands that the client has accepted continue running.

`internal/service` authenticates callers, routes terminal requests and translates client responses for MCP. It attaches device IDs to session listings. See the [MCP tool reference](mcp-tools.md) for filtering, output limits and control arguments.

A session ID identifies a terminal on one device. When multiple devices are connected, use `client_id` and `session_id` together. Separate devices can use the same session name.

The session manager lives in the client process outside the reconnect loop. It retains sessions and cached results through temporary network failures. Reconnecting the same process makes them accessible again without replaying commands. Explicit client shutdown or permanent rejection closes its terminals. Closed results remain readable until the client’s retention policy removes them.
