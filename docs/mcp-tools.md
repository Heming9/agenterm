# MCP tool reference

[简体中文](mcp-tools.zh-CN.md) · [User guide](usage.md)

This reference describes the current source. Deployed or older releases may use earlier schemas, such as `client_token` for the device credential. Consult the connected server's `tools/list` for its contract.

## Access modes

| Mode | HTTP credential | Tool calls |
| --- | --- | --- |
| Management | `Authorization: Bearer <management key>`. Omit the header when the server runs with `-no-password`. | Eight tools including `create_client`. All other tools require a `control_token` argument. |
| Scoped | `Authorization: Bearer <control_token>` | Seven tools scoped to the devices under that credential. Omit the `control_token` argument. |

The target client connects with the `device_token` returned by `create_client`. The agent keeps the `control_token` private for execution and management. For a self-hosted MCP configuration example, see the [user guide](usage.md#connect-your-agent).

## Tools

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `device_token`, `control_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_client` | List online devices or wait for a connection. | `clients`; empty means no online device when returned. |
| `close_client` | Close selected devices or all online devices for the token. | Closed `client_ids`. |
| `p2p_transfer_start` | Bootstrap p2p-cli remotely and return Agent-side scripts after remote signaling registration. | `connection_id`, endpoint IDs, `state`, `shell_script`, `powershell_script`. |
| `execute_command` | Create through setup or reuse through session_id; anonymous commands close quickly or retain on wait expiry. | `command_id`, `status`, `output`, `next_cursor`, `has_more`; optional `session_id`, `exit_code`, `error`, `truncated`. |
| `session_read` | Read paginated output using the same result format as execute_command. | `session_id`, `command_id`, `status`, `output`, `next_cursor`, `has_more`, and optional `exit_code`, `error`, `truncated`. |
| `session_control` | Input, interrupt, EOF or stop selected by action. | Input/interrupt/EOF return `ok:true`; stop returns `status` and optional `exit_code/error`. |
| `session_list` | Find retained sessions grouped by device, in creation order within each device. | `sessions`: `session_id`, `client_id`, `command`, `command_id`, `status`; optional `exit_code/error`. |

## Common usage

Create an invitation and share its connection instructions privately. Once the device connects, use `execute_command`. This management call runs an anonymous command and closes its terminal on completion.

```json
{"control_token":"CONTROL_TOKEN","command":"pwd"}
```

The remaining examples use scoped access. With a multi-device credential, also pass `client_id` to `execute_command`, `session_read` and `session_control`.

Use `execute_command` to create a named terminal. After the command finishes, reuse the directory and environment via the top-level `session_id`.

```json
{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}
```

```json
{"session_id":"build","command":"git status"}
```

When the active command requests input, call `session_control`.

```json
{"session_id":"SESSION_ID","action":"input","input":"yes\n"}
```

## Details

<details>
<summary>Invitations and display</summary>

The MCP text content of `create_client` directly returns a fixed English connection template with Markdown code blocks around the commands, identical to `structuredContent.display_text`, regardless of the server language setting. Credentials, expiry and platform commands remain available in `structuredContent` for programmatic use. The template contains the Windows command, macOS / Linux command, connection token with the short code and browser invitation message when enabled, usage steps and invitation validity in that order. Multi-device mode appends an invitation reuse notice.

When short codes are enabled for a single-device invitation, `short_code_url` is a private browser link (for example, `example.com/c/abcdef`) to the connection instructions. The link and code are unavailable while the device is online and become usable again after disconnection while still valid. The link follows the short code’s lifetime and becomes invalid after the tenth successful redemption. GET opens the invitation page and POST redeems the short code. Installer scripts use a separate device-token secret in the path.

1. Present every section of `display_text` in order and in the user's language. Preserve commands, URLs, tokens and short codes exactly. Put each command, the device token and the browser invitation message in separate Markdown code blocks.
2. Express lifetimes in readable units. Convert `expires_at`, `token_initial_expires_at` and any `short_code_expires_at` to the user's known time zone and label it. Use UTC when the time zone is unknown.
3. Keep the `control_token` private. Its format is `device_token:key`. After presenting the invitation, use it to poll `list_client` with `wait_ms: 30000` within a shared 300-second wall-clock budget. Proceed as soon as a device comes online. If a poll is empty, keep waiting within the remaining budget. At the deadline, check once more with `wait_ms: 0` before reporting a timeout.
4. On a transient network error, retry the check once with `wait_ms: 0`. If the check still fails, report that connectivity is unknown. Retries share the original budget. Stop on user cancellation or invalid, expired or revoked credentials, and report credential errors separately.

The frontend accepts legacy wait values up to `300000` and caps each poll at 30000 ms.

</details>

<details>
<summary>Device information</summary>

`list_client` contains online devices only. Each entry retains client_id, connected_at and client_info, without top-level or per-device status. Array membership indicates connectivity. Hostname, platform, architecture, shell, default directory, client version and capability metadata remain available. Heartbeat/RTT remain internal and available to the client UI; they are not embedded in MCP status strings.

`close_client` accepts a nonempty array of unique `client_ids`; omit it to close all online devices and revoke the token and its invitations, even when no devices are online. Every ID must belong to that token, and validation failure closes none. The result lists the closed IDs, or an empty array when none are online. Closing a device ends its sessions and active transfers; selecting IDs preserves the token and invitations. Closed clients do not automatically reconnect.

</details>

See the [terminal client API](client-api.md) for session lifecycle, result retention and WebSocket forwarding. Device sessions can be queried once the device is online.

<details>
<summary>Commands and interactive sessions</summary>

Top-level `execute_command.session_id` only reuses an existing terminal and rejects unknown/closed IDs. `setup` always creates a terminal and contains optional `cwd`, `env` and `session_id`; an existing setup ID is rejected, never overwritten or reused. setup and top-level session_id cannot be combined. An explicit setup ID creates a persistent terminal; omit that ID for a generated anonymous terminal. Omitting both also creates an anonymous terminal: fast completion closes it; wait expiry retains the same shell. IDs are unique within each client. For multi-device tokens, execute_command, session_read and session_control always require client_id, including when reusing an existing session. Different devices may use the same session_id.

Directory, variables, functions and environment activation persist within the shell. `cwd`/`env` are available only inside setup to initialize new terminals. Reuse without setup; change state with cd/export or shell equivalents. Each terminal accepts one command at a time and returns session_busy when occupied, without queueing or opening a replacement.

For ordinary commands, keep the default 10-second wait; completion returns early. Reuse idle shells for related commands. When waiting for new output, prefer `session_read(wait_ms=10000–20000)`. Drain output, then stop task-owned shells that are no longer needed. At capacity, use `session_list(status="idle")` to find reusable shells or `status="live"` to inspect cleanup candidates; confirm `closed` after stopping a shell before creating its replacement.

`wait_ms` defaults to 10000 (10 seconds), range 0–20000, from client admission including shell startup. Zero immediately retains anonymous terminals while still waiting for admission. Expiry never kills execution. Results use running while busy, idle after command completion in a retained shell, and closed once the client confirmed shell termination. Named and retained shells require explicit session_control(action=stop), with no idle expiry. Canceling an MCP waiter does not cancel accepted work.

execute_command returns command_id, status, output, next_cursor and has_more. It includes session_id for named or previously retained terminals, and for anonymous terminals after wait expiry or when output exceeds the response limit or retained history was truncated. Execution returns up to 16 KiB of UTF-8 output; has_more=true means the remaining output is available through session_read. A completed anonymous shell stays closed, but its ID can read client-cached output. session_read always includes session_id. status is running (busy, including pending stop confirmation), idle (command finished and shell reusable), or closed (the client confirmed shell termination). Fast anonymous commands finish closed; completed named/retained commands become idle. Completion adds exit_code including successful 0; error appears only on failure and truncated only as true when old output was lost.

Linux/macOS use the reported shell and a PTY. Windows uses PowerShell and pipes for basic input interaction without full terminal emulation. Persistent terminals inherit client environment but do not load user shell startup files, so the integration remains reliable. Prompt hooks/PS1 are reserved; do not override them or terminal echo settings. Initialize additional environments explicitly in commands. Both server and client must support the current client API version; the handshake advertises and checks that version. There is no legacy execution protocol.

Each client process permits at most 10 live terminals, including starting, running, idle and closing shells until the OS process exits. Multiple connections within a process share the limit. Other processes using the same token have independent limits. At capacity, anonymous creation or a missing named terminal returns a terminal-limit error. Explicitly selecting an existing terminal by session_id still permits reuse; another terminal is never chosen automatically. Closed output history consumes no slots. The client owns output-cache retention. Restarting the server frontend preserves runtime-owned connections; restarting the server runtime loses server credentials and connections, without synthesizing terminal results.

</details>

<details>
<summary>Reading output and finding sessions</summary>

`session_read.max_bytes` defaults to 16384, range 4–65536 bytes, preserving UTF-8 boundaries. Continue from next_cursor and drain has_more even after idle/closed. Empty output or a drained page does not prove completion; inspect status. truncated appears only for discarded older output, never merely for pagination.

`session_list` queries online clients, optionally filtered by `client_id` and `status`: `all` (default), `live` (running or idle), `running`, `idle`, or `closed`. Use `idle` for reuse and `live` to inspect capacity. Entries contain `session_id`, `client_id`, `command`, `command_id`, `status`, and optional `exit_code`/`error`. Command previews are limited to 160 Unicode characters, including a `... (truncated)` suffix when shortened. Closed history consumes no terminal slots; each client retains at most 10 closed records for up to 30 minutes.

New command IDs use eight case-sensitive alphanumeric characters. Pass command IDs from older clients unchanged.

Optionally pass `command_id` to `session_read` to pin its status to that command; if a newer command has started, the read fails rather than mislabeling the result. Omit it to read retained session output by byte cursor across commands. Client-owned retained terminal buffers keep the latest 1 MiB; discarding older requested output sets `truncated=true`.

</details>

<details>
<summary>Session control: input, interrupt, EOF and stop</summary>

`session_control` requires session_id and action. Input targets only the active command; idle shells reject it, and new commands use execute_command. Only input requires/accepts input text, and only stop accepts optional wait_ms (default 1000). Input, interrupt and EOF share `{"ok":true}` after client acknowledgment, without claiming that a program consumed input or handled a signal. Stop returns status and optional exit_code/error, with no action echo.

Before interrupt or EOF, check `list_client.client_info.session_controls` (`interrupt` and/or `eof`). An empty list means unavailable; older clients may need updating. Both operations wait for the client API response; request cancellation ends only the caller’s wait. `ok=true` does not guarantee the program handled the operation; read output to verify. After a timeout or cancellation, check output before retrying input or signals because delivery is uncertain.

- `session_control(action="interrupt")` sends SIGINT to the current terminal foreground process group on Linux/macOS. An interactive shell may remain usable after its foreground command exits; the program may also ignore the signal or exit. This does not forcibly cancel the entire session like `session_control(action="stop")`. Windows pipe mode does not support interrupt.
- `session_control(action="eof")` follows previously queued input. On Linux/macOS canonical terminals it sends the configured VEOF character: pending input may be delivered first, requiring another EOF, and stdin is not permanently closed. Raw mode or disabled VEOF returns an error. On Windows pipes it permanently closes stdin while output remains readable; subsequent writes fail and repeated EOF is harmless. EOF at an idle shell prompt may exit the shell.

`session_control(action="stop")` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

| Status | Meaning and next step |
| --- | --- |
| `running` | Stop has been sent or remains pending. Continue reading state; remote termination is not confirmed. |
| `closed` | The client confirmed shell termination. Read retained output; a disconnection error means remote termination remains unconfirmed. |

Repeated stop is safe; the client confirms termination and returns its stored shell result after completion. Unknown/unauthorized sessions remain errors. A transport disconnection returns an error without inventing status=closed or an exit code. Stop cannot undo completed operations, revoke credentials or stop other sessions.

</details>
