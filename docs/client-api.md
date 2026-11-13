# Terminal client API

All terminal and connection code lives in `internal/client`, organized by file:

- `api.go` and `api_validation.go` define the transport-independent interface, typed requests/results, validation and limits.
- `manager.go`, `history.go` and the process/shell files implement terminal execution, IDs, admission, waiting, input, output caching and retention. CLI and GUI use the same Manager, whose lifecycle is independent of transport connections.
- `relay.go`, `api_transport.go` and `reconnect.go` handle server connections and adapt requests to the Manager. Future P2P adapters can call the same API.
- `api_dispatch.go` decodes a JSON `APIRequest` and dispatches it to any implementation of `client.API`.

`protocol.Message` carries `api_call`, `api_cancel` and `api_result` envelopes with opaque JSON request/result bodies. The protocol package has no dependency on client types. P2P bootstrap output stays in local logs/UI callbacks; only a `p2p_done` notification with the bootstrap ID is sent when it ends. Cancellation cancels the response waiter, not accepted execution. The client API version is negotiated in the handshake.

`internal/service` authenticates callers, selects connected devices and forwards client API requests. MCP translates tool arguments and results. The server keeps no terminal registry, output cache, terminal cleanup loop or inferred exit status. `session_list` queries connected clients and attaches device routing IDs to their results. MCP supports status filtering and 160-character command previews, with `... (truncated)` appended when shortened.

A session ID is local to one manager. Multi-device calls identify their target with `client_id` and `session_id`; separate devices may use the same session name. A direct P2P adapter can dispatch the same `client.APIRequest` to the same manager without reproducing any server-side terminal logic.

Temporary transport failure preserves the manager and accepted commands. Reconnect exposes the same sessions and cached results without replay. Explicit client shutdown or permanent rejection closes the manager. Closed results remain readable until the client’s retention policy removes them; transport failure never changes a confirmed command exit code.

Runtime callbacks are delivered outside terminal locks. `Close` closes processes and the event stream; owners may call `WaitCallbacks` afterward to drain notifications. Keep that drain outside callbacks. Output observer queues apply bounded backpressure and unblock on shutdown.

New command IDs use eight case-sensitive alphanumeric characters; older command IDs remain accepted.
