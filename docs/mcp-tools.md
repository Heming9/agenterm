# MCP tool reference

Current source builds publish explicit output schemas and behavior annotations for all eight tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `control_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_client` | List online devices or wait for a connection. | `clients`; empty means no online device when returned. |
| `close_client` | Close selected devices or all online devices for the token. | Closed `client_ids`. |
| `p2p_transfer_start` | Bootstrap p2p-cli remotely and return Agent-side scripts after remote signaling registration. | `connection_id`, endpoint IDs, `state`, `shell_script`, `powershell_script`. |
| `execute_command` | Create through setup or reuse through session_id; anonymous commands close quickly or retain on wait expiry. | `command_id`, `status`, `output`, `next_cursor`, `has_more`; optional `session_id`, `exit_code`, `error`, `truncated`. |
| `session_read` | Read paginated output using the same result format as execute_command. | Six regular fields; completion adds exit_code, with error/truncated only when needed. |
| `session_control` | Input, interrupt, EOF or stop selected by action. | Input/interrupt/EOF return `ok:true`; stop returns `status` and optional `exit_code/error`. |
| `session_list` | Find retained sessions in creation order, optionally filtered by client_id. | `sessions`: `session_id`, `client_id`, `command`, `command_id`, `status`; optional `exit_code/error`. |


## Common usage

Create an invitation and share its connection command privately. Once the device connects, execute commands. Specify session_id through execute_command to preserve shell state across subsequent commands. When the current command requests input, use session_control:

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

Use the connected server’s `tools/list` as the source of truth for tools and fields.

Fast anonymous command (automatically closed on completion):

```json
{"command":"pwd"}
```

Independent terminal for a long task:

```json
{"command":"npm run build","setup":{"cwd":"/project","session_id":"build"},"wait_ms":1000}
```

Reuse the terminal:

```json
{"session_id":"…","command":"git status"}
```


## Details

<details>
<summary>Invitations and display</summary>

The MCP text content of `create_client` directly returns a fixed English connection template with Markdown code blocks around the commands, identical to `structuredContent.display_text`, regardless of the server language setting. Credentials, expiry and platform commands remain available in `structuredContent` for programmatic use. The template contains the Windows command, macOS / Linux command, connection token, short code and short link when enabled, usage steps and invitation validity in that order. Multi-device mode appends an invitation reuse notice.

When short codes are enabled for a single-device invitation, `short_code_url` is a private browser link (for example, `example.com/c/abcdef`) to the connection instructions. The link and code are unavailable while the device is online and become usable again after disconnection while still valid. The link follows the short code’s lifetime and becomes invalid after the tenth successful redemption. Viewing it does not count as a redemption.

After generating connection instructions with `create_client`, the agent must present all sections of `display_text` in order and in the user’s language. Commands, URLs and connection tokens must remain unchanged; each command must be placed in its own Markdown code block for reliable copying. Express the lifetime in equivalent readable units, such as 5 minutes for 300 seconds. When the user’s time zone is known, convert `expires_at` to that time zone and label it explicitly; otherwise show the UTC deadline. Do not guess the time zone from the user’s language or the server settings. Keep `control_token` private: it has the form `client_token:key` and must never be shared with the target device or included in connection instructions. Then poll `list_client` with `client_token` set to the returned `control_token`, using separate calls with `wait_ms: 30000` within a total 300-second wall-clock budget. Each call returns immediately when any device is online. An empty poll means only that no device was online for that poll; continue with the remaining budget. Once the budget is exhausted, make a final `wait_ms: 0` check before reporting a connection-wait timeout. On a transient network error, recheck once with `wait_ms: 0`; if that also fails, report that the check failed and device connectivity is unknown. Retries do not reset the budget. Stop on user cancellation or invalid, expired or revoked credentials, reporting credential errors separately. Legacy values through `300000` remain accepted but each call is capped at 30000 ms by the frontend, including when it forwards to a retained older runtime. These requirements are published in the MCP tool descriptions.

</details>

<details>
<summary>Device information</summary>

`list_client` contains online devices only. Each entry retains client_id, connected_at and client_info, without top-level or per-device status. Array membership indicates connectivity. Hostname, platform, architecture, shell, default directory, client version and capability metadata remain available. Heartbeat/RTT remain internal and available to the client UI; they are not embedded in MCP status strings.

`close_client` accepts a nonempty array of unique `client_ids`; omit it to close all online devices and revoke the token and its invitations, even when no devices are online. Every ID must belong to that token, and validation failure closes none. The result lists the closed IDs, or an empty array when none are online. Closing a device ends its sessions and active transfers; selecting IDs preserves the token and invitations. Closed clients do not automatically reconnect. Management access requires `client_token` to contain the `control_token` (`token:key`); scoped access uses the same control credential as its bearer token. A device token alone cannot call control tools.

</details>

<details>
<summary>Responsibility and state ownership</summary>

| Responsibility | Owner |
| --- | --- |
| Terminal IDs, creation/reuse, busy and input validation | Shared client execution core for CLI and GUI |
| Ten live-terminal limit, processes, retention on wait expiry and closure | Client |
| Authorization, device selection and RPC forwarding | Server |
| Result waiting, output caching, pagination and finished-result retention | Client; result cleanup never closes a terminal |

`client/api.go` defines the terminal API and data contracts independently of transport. `client.Manager` implements execution, validation, waiting, control, output storage and retention. All client code lives in the same package, organized by file. The WebSocket adapter forwards requests to that same API; a future direct P2P adapter can call it unchanged. The server holds connection and request-correlation state only. It does not index sessions, infer completion, enforce terminal quotas or clean up terminal results.

The manager belongs to the client process, outside the reconnect loop. A temporary transport loss cancels response waiters while accepted commands, shell state and output remain available. Reconnect can query the same manager through session_list/session_read without replaying commands. Explicit client shutdown or permanent disconnection closes its manager. Canceling a result waiter does not stop its command. Offline clients cannot be queried.

</details>

<details>
<summary>Commands and interactive sessions</summary>

Top-level `execute_command.session_id` only reuses an existing terminal and rejects unknown/closed IDs. `setup` always creates a terminal and contains optional `cwd`, `env` and `session_id`; an existing setup ID is rejected, never overwritten or reused. setup and top-level session_id cannot be combined. An explicit setup ID creates a persistent terminal; omit that ID for a generated anonymous terminal. Omitting both also creates an anonymous terminal: fast completion closes it; wait expiry retains the same shell. IDs are unique within each client. For multi-device tokens, execute_command, session_read and session_control always require client_id, including when reusing an existing session. Different devices may use the same session_id.

Directory, variables, functions and environment activation persist within the shell. `cwd`/`env` are available only inside setup to initialize new terminals. Reuse without setup; change state with cd/export or shell equivalents. Each terminal accepts one command at a time and returns session_busy when occupied, without queueing or opening a replacement.

For ordinary commands, keep the default 10-second wait; completion returns early. Reuse idle shells for related commands. When waiting for new output, prefer `session_read(wait_ms=10000–20000)`. Drain output, then stop task-owned shells that are no longer needed. At capacity, use `session_list(status="idle")` to find reusable shells or `status="live"` to inspect cleanup candidates; confirm `closed` after stopping a shell before creating its replacement.

`wait_ms` defaults to 10000 (10 seconds), range 0–20000, from client admission including shell startup. Zero immediately retains anonymous terminals while still waiting for admission. Expiry never kills execution. Results use running while busy, idle after command completion in a retained shell, and closed once the client confirmed shell termination. Named and retained shells require explicit session_control(action=stop), with no idle expiry. Canceling an MCP waiter does not cancel accepted work.

execute_command returns command_id, status, output, next_cursor and has_more. It includes session_id for named or previously retained terminals, and for anonymous terminals after wait expiry or when output exceeds the response limit or retained history was truncated. Execution returns up to 16 KiB of UTF-8 output; has_more=true means the remaining output is available through session_read. A completed anonymous shell stays closed, but its ID can read client-cached output. session_read always includes session_id. status is running (busy, including pending stop confirmation), idle (command finished and shell reusable), or closed (the client confirmed shell termination). Fast anonymous commands finish closed; completed named/retained commands become idle. Completion adds exit_code including successful 0; error appears only on failure and truncated only as true when old output was lost.

Linux/macOS use the reported shell and a PTY. Windows uses PowerShell and pipes for basic input interaction without full terminal emulation. Persistent terminals inherit client environment but do not load user shell startup files, so the integration remains reliable. Prompt hooks/PS1 are reserved; do not override them or terminal echo settings. Initialize additional environments explicitly in commands. Both server and client must support the current client API version; the handshake advertises and checks that version. There is no legacy execution protocol.

Each client process permits at most 10 live terminals, including starting, running, idle and closing shells until the OS process exits. CLI/GUI share the same client execution layer and quota; multiple connections within a process share the limit. Other processes using the same token have independent limits. At capacity, anonymous creation or a missing named terminal returns a terminal-limit error. Explicitly selecting an existing terminal by session_id still permits reuse; another terminal is never chosen automatically. Closed output history consumes no slots. The client owns output-cache retention. Temporary transport disconnection preserves terminals and results in the client manager; reconnect does not replay commands. Explicit client shutdown or a permanent rejection closes its terminals. Restarting the server frontend preserves runtime-owned connections; restarting the server runtime loses server credentials and connections, without synthesizing terminal results.

</details>

<details>
<summary>Reading output and finding sessions</summary>

`session_read.max_bytes` defaults to 16384, range 4–65536 bytes, preserving UTF-8 boundaries. Continue from next_cursor and drain has_more even after idle/closed. Empty output or a drained page does not prove completion; inspect status. truncated appears only for discarded older output, never merely for pagination.

`session_list` queries online clients, optionally filtered by `client_id` and `status`: `all` (default), `live` (running or idle), `running`, `idle`, or `closed`. Use `idle` for reuse and `live` to inspect capacity. Entries contain `session_id`, `client_id`, `command`, `command_id`, `status`, and optional `exit_code`/`error`. Command previews are limited to 160 Unicode characters, including a `... (truncated)` suffix when shortened. Closed history consumes no terminal slots; each client retains at most 10 closed records for up to 30 minutes.

New command IDs use eight case-sensitive alphanumeric characters. Pass command IDs from older clients unchanged.

Optionally pass `command_id` to `session_read` to pin its status to that command; if a newer command has started, the read fails rather than mislabeling the result. Omit it to read retained session output by byte cursor across commands. Client-owned retained terminal buffers keep the latest 1 MiB; dropping older requested output sets `truncated=true`, while pagination alone does not. Drain `has_more` even after command completion.

</details>

<details>
<summary>Session control: input, interrupt, EOF and stop</summary>

`session_control` requires session_id and action. Input targets only the active command; idle shells reject it, and new commands use execute_command. Only input requires/accepts input text, and only stop accepts optional wait_ms (default 1000). Input, interrupt and EOF share `{"ok":true}` after client acknowledgment, without claiming that a program consumed input or handled a signal. Stop returns status and optional exit_code/error, with no action echo. Input/signals may repeat side effects: do not blindly retry uncertain delivery.

Before interrupt or EOF, check `list_client.client_info.session_controls` (`interrupt` and/or `eof`). An empty list means unavailable; older clients may need updating. Both operations wait for the client API response; request cancellation ends only the caller’s wait. `ok=true` does not guarantee the program handled the operation; read output to verify. A timeout or cancellation leaves delivery uncertain, so do not blindly retry.

- `session_control(action="interrupt")` sends SIGINT to the current terminal foreground process group on Linux/macOS. An interactive shell may remain usable after its foreground command exits; the program may also ignore the signal or exit. This does not forcibly cancel the entire session like `session_control(action="stop")`. Windows pipe mode does not support interrupt.
- `session_control(action="eof")` follows previously queued input. On Linux/macOS canonical terminals it sends the configured VEOF character: pending input may be delivered first, requiring another EOF, and stdin is not permanently closed. Raw mode or disabled VEOF returns an error. On Windows pipes it permanently closes stdin while output remains readable; subsequent writes fail and repeated EOF is harmless. EOF at an idle shell prompt may exit the shell.

`session_control(action="stop")` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

| Status | Meaning and next step |
| --- | --- |
| `running` | Stop has been sent or remains pending. Continue reading state; remote termination is not confirmed. |
| `closed` | The client confirmed shell termination. Read retained output; a disconnection error means remote termination remains unconfirmed. |


Repeated stop is safe; the client confirms termination and returns its stored shell result after completion. Unknown/unauthorized sessions remain errors. A transport disconnection returns an error without inventing status=closed or an exit code. Stop cannot undo completed operations, revoke credentials or stop other sessions.

</details>
