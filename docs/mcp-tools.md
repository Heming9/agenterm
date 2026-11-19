# MCP tool reference

[简体中文](mcp-tools.zh-CN.md) · [User guide](usage.md) · [Terminal client API](client-api.md)

The connected server's `tools/list` publishes the schemas and behavior annotations. Management access exposes all eight tools and requires the invitation's `control_token` as an argument to device tools. Scoped access exposes the seven device tools and uses that credential as the HTTP Bearer token.

| Tool | When to use it |
| --- | --- |
| `create_client` | Create a private connection invitation. |
| `list_client` | Check which devices are online or wait for one to connect. |
| `close_client` | Close selected devices. Omit `client_ids` to close all online devices under the token and revoke the token and its invitations, even if no devices are online. |
| `execute_command` | Run a command in a new or existing terminal. |
| `session_read` | Read more output and check command status. |
| `session_list` | Find retained sessions on online devices. |
| `session_control` | Send input, interrupt or EOF, or stop a terminal. |
| `p2p_transfer_start` | Start the remote helper for a direct file transfer. |

## Invitations and devices

`create_client` returns `display_text` as both the MCP text content and a structured field, alongside the private `control_token` and UTC RFC 3339 deadline `expires_at`. Present the entire `display_text`, translating only its natural-language prose. Do not paraphrase, summarize, add, omit, or reorder content. Preserve code blocks and copy commands, URLs, device tokens, and short codes verbatim. Show each original URL as visible text, not as a Markdown link label. Show the deadline in the user's time zone when known or in UTC otherwise, and label the zone.

Keep `control_token` in the host's private storage for the connection so later turns can reuse it, including after a wait times out. Keep it out of logs, user-facing messages and invitation material. The recipient uses the link or commands from `display_text`.

The invitation lifetime defaults to 900 seconds. Set `multi_device: true` to allow simultaneous devices. A single-device invitation uses a short-code link when enabled. See the [invitation and short-code rules](usage.md#invitation-lifetime-and-short-codes) for reuse and redemption. The browser link also serves the installer with the `.sh` or `.ps1` suffix. Set `temporary_assistance: true` for a temporary installation with terminal access only.

### Wait for a device

`list_client` returns a `clients` array of online devices, each with `client_id`, `connected_at` and `client_info`. Use `wait_ms: 0` for an immediate check. Positive values return early when a device is online. Each call waits at most 30 seconds, including when it accepts a legacy value up to `300000` ms.

1. After presenting the invitation, start a five-minute wall-clock budget. Poll `list_client` using the returned `control_token`. Set `wait_ms` to the remaining time in milliseconds, capped at `30000` per poll.
2. Stop as soon as `clients` contains a device. An empty array means no device was online when that call returned. Keep polling within the remaining budget.
3. On a transient network error, recheck once with `wait_ms: 0` within the same budget. If the recheck fails, report that the check failed and device connectivity is unknown. Stop on cancellation or a credential error. Report invalid, expired or revoked credentials separately.
4. At the end of the budget, check once more with `wait_ms: 0`. Report a connection timeout only if this check succeeds with an empty array.

### Close devices

Pass a nonempty list of unique `client_ids` to close only those devices and keep the token and its invitations. All IDs must belong to the token. A validation failure closes no devices. Omit `client_ids` to close all online devices under the token and revoke the token and its invitations, even if no devices are online. The result lists the closed `client_ids`. Closing a device ends its sessions and active transfers.

## Terminal sessions

For multi-device tokens, pass `client_id` to `execute_command`, `session_read` and `session_control`. Session IDs are local to each device.

- Use `setup` to create a terminal with optional `cwd`, `env` and `session_id`. An explicit `setup.session_id` must be unused and creates a persistent terminal.
- Use a top-level `session_id` to reuse a live terminal and preserve shell state. It cannot be combined with `setup`. Busy terminals return `session_busy`.
- Without an explicit ID, execution uses an anonymous terminal. It closes if the command finishes within the wait and stays open if the wait expires first. `wait_ms` defaults to `10000` and accepts `0–20000` ms from client admission. Use `0` to retain an anonymous terminal immediately. An accepted command can continue after the caller stops waiting.

Each client process allows up to 10 live terminals. Named and retained terminals have no idle expiry. Reuse an appropriate idle terminal or explicitly stop one when the task is done. Closed history consumes no live slots.

| Status | Meaning |
| --- | --- |
| `running` | The terminal is busy or a stop is pending. |
| `idle` | The command finished and the terminal can be reused. |
| `closed` | The client confirmed the shell terminated. Retained output may still be readable. |

### Execute, read and finish

The examples assume scoped access to a single device.

1. Call `execute_command` with `{"command":"pwd"}` for a quick command or `{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}` to keep a named terminal.
2. If the command is still running or `has_more=true`, call `session_read` with the returned `session_id` and `next_cursor` as `cursor`. Pass `command_id` to reject the read if a newer command has started. Read until `has_more=false`, even after `idle` or `closed`. If still `running` with no unread output, use `wait_ms` up to `20000` to wait for new output.
3. Reuse an idle terminal with `execute_command` and `{"session_id":"build","command":"git status"}`, or stop it with `session_control` and `{"session_id":"build","action":"stop"}`. If stop is pending, check for `closed` before assuming the slot is free.

Execution returns up to 16 KiB of UTF-8 output. It returns `session_id` for named or retained terminals and for closed anonymous terminals with more or truncated output. Reads always include `session_id`. `max_bytes` defaults to 16384 and accepts 4–65536 bytes. Reads preserve UTF-8 boundaries. The retained buffer holds the latest 1 MiB. `truncated=true` means older requested output was discarded. Command completion adds `exit_code`, including 0 on success. A transport failure leaves the outcome unconfirmed.

`session_list` can filter by `client_id` or by status. Use `idle` to find reusable terminals, `live` for `running` and `idle`, or `all` (the default) to include closed history. Each client retains up to 10 closed records for up to 30 minutes. Command previews are capped at 160 Unicode characters. A temporary disconnect can preserve sessions and results on the client. After reconnecting, check them before submitting a new command. See the [terminal client API](client-api.md).

### Control a session

`session_control` requires `session_id` and `action`.

- `input` requires text in `input` and sends it verbatim to the active command. Include a newline if needed, for example `{"session_id":"build","action":"input","input":"yes\n"}`.
- Before `interrupt` or `eof`, check `list_client.client_info.session_controls`. On Linux/macOS, interrupt sends SIGINT to the foreground process group and EOF sends VEOF in canonical mode. Windows pipe mode supports EOF, which permanently closes stdin.
- Only `stop` accepts `wait_ms` (default `1000`, range `0–20000`). It returns `running` while pending or `closed` when confirmed, with a shell exit code or error when available. Repeated stop is safe.

For input, interrupt and EOF, `ok: true` confirms receipt by the client. Check output to verify the program's response. Avoid retrying input or signals after uncertain delivery. Initialize any required shell environment explicitly in retained terminals and preserve reserved prompt hooks and echo settings.

## File transfer

Use `p2p_transfer_start` with a standard invitation. Pass `client_id` for multi-device tokens. `waiting_for_agent` means the remote helper registered. Run the private script for the Agent's execution environment, then use the returned `executable` path to check for `connected` before copying. See the [P2P component guide](../components/p2p-cli/README.md).
