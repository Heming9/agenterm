# MCP tool reference

Current source builds publish explicit output schemas and behavior annotations for all seven tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_clients` | List online devices or wait for a connection. | `clients`; empty means no online device when returned. |
| `p2p_transfer_start` | Bootstrap p2p-cli remotely and return Agent-side scripts after remote signaling registration. | `connection_id`, endpoint IDs, `state`, `shell_script`, `powershell_script`. |
| `execute_command` | Create through setup or reuse through session_id; anonymous commands close quickly or retain on wait expiry. | `session_id`, `command_id`, `status`, `output`, `next_cursor`, `has_more`; optional `exit_code`, `error`, `truncated`. |
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

The MCP text content of `create_client` directly returns a fixed English plain-text connection template, identical to `structuredContent.display_text`, regardless of the server language setting. Credentials, expiry and platform commands remain available in `structuredContent` for programmatic use. The template contains the Windows command, macOS / Linux command, connection token, usage steps and invitation validity in that order. Multi-device mode appends an invitation reuse notice.

After generating connection instructions with `create_client`, the agent must present the complete `display_text` in the language used to communicate with the user. Translate only headings and explanatory prose; when communicating in English, present it verbatim. Preserve all sections, order, line breaks and plain-text format. Keep commands, URLs, connection tokens, validity numbers and expiry timestamps unchanged. Do not omit content or add Markdown formatting. Then call `list_clients` with the returned `client_token` and `wait_ms: 300000` to check for a successful connection, with a 300-second timeout. The call returns immediately when any device is online. If no device connects within 300 seconds, report the timeout to the user. These requirements are published in the MCP tool descriptions.

</details>

<details>
<summary>Device information</summary>

`list_clients` contains online devices only. Each entry retains client_id, connected_at and client_info, without top-level or per-device status. Array membership indicates connectivity. Hostname, platform, architecture, shell, default directory, client version and capability metadata remain available. Heartbeat/RTT remain internal and available to the client UI; they are not embedded in MCP status strings.

</details>

<details>
<summary>Responsibility and state ownership</summary>

| Responsibility | Owner |
| --- | --- |
| Terminal IDs, creation/reuse, busy and input validation | Shared client execution core for CLI and GUI |
| Ten live-terminal limit, processes, retention on wait expiry and closure | Client |
| Authorization, device routing, request correlation, waiting and output caching | Server |
| Finished-record retention and cache capacity | Server; cache cleanup never closes a terminal |

The client reports its terminal ID, creation/reuse decision and retention policy before output starts. The server mirrors these facts and never selects or keeps terminals alive based on cached state. Late admission remains discoverable after waiter cancellation. The client alone decides whether anonymous completion closes the shell or waiting expiry retains it; server cache cleanup never closes terminals.

</details>

<details>
<summary>Commands and interactive sessions</summary>

Top-level `execute_command.session_id` only reuses an existing terminal and rejects unknown/closed IDs. `setup` always creates a terminal and contains optional `cwd`, `env` and `session_id`; an existing setup ID is rejected, never overwritten or reused. setup and top-level session_id cannot be combined. An explicit setup ID creates a persistent terminal; omit that ID for a generated anonymous terminal. Omitting both also creates an anonymous terminal: fast completion closes it; wait expiry retains the same shell. IDs are unique within a token; multi-device creation requires client_id, while existing IDs identify their devices.

Directory, variables, functions and environment activation persist within the shell. `cwd`/`env` are available only inside setup to initialize new terminals. Reuse without setup; change state with cd/export or shell equivalents. Each terminal accepts one command at a time and returns session_busy when occupied, without queueing or opening a replacement.

`wait_ms` defaults to 10000 (10 seconds), range 0–20000, from client admission including shell startup. Zero immediately retains anonymous terminals while still waiting for admission. Expiry never kills execution. Results use running while busy, idle after command completion in a retained shell, and closed once the session is no longer usable. Named and retained shells require explicit session_control(action=stop), with no idle expiry. Canceling an MCP waiter does not cancel accepted work.

execute_command and session_read share six regular fields: session_id, command_id, status, output, next_cursor and has_more. status is running (busy, including pending stop confirmation), idle (command finished and shell reusable), or closed (unusable; a disconnection leaves remote termination unconfirmed). Fast anonymous commands finish closed; completed named/retained commands become idle. Completion adds exit_code including successful 0; error appears only on failure and truncated only as true when old output was lost. Removed execution/read fields: session_created, command_done, done, session_alive, persistent, promoted, timed_out and canceled.

Linux/macOS use the reported shell and a PTY. Windows uses PowerShell and pipes for basic input interaction without full terminal emulation. Persistent terminals inherit client environment but do not load user shell startup files, so the integration remains reliable. Prompt hooks/PS1 are reserved; do not override them or terminal echo settings. Initialize additional environments explicitly in commands. Both server and client must be updated; older clients receive an upgrade error.

Each client process permits at most 10 live terminals, including starting, running, idle and closing shells until the OS process exits. CLI/GUI share the same client execution layer and quota; multiple connections within a process share the limit. Other processes using the same token have independent limits. At capacity, anonymous creation or a missing named terminal returns a terminal-limit error. Explicitly selecting an existing terminal by session_id still permits reuse; another terminal is never chosen automatically. Closed output history consumes no slots. The server applies only output-cache retention, never terminal quotas or idle cancellation. Disconnect terminates the connection’s terminals; reconnect loses shell state. Restarting the server frontend preserves runtime-owned connections and terminals; restarting the runtime loses tokens, invitations and sessions.

</details>

<details>
<summary>Reading output and finding sessions</summary>

`session_read.max_bytes` defaults to 16384, range 4–65536 bytes, preserving UTF-8 boundaries. Continue from next_cursor and drain has_more even after idle/closed. Empty output or a drained page does not prove completion; inspect status. truncated appears only for discarded older output, never merely for pagination.

`session_list` returns this token’s retained sessions in ascending creation order, including closed records, with optional client_id filtering. Each entry has session_id, client_id, command, command_id and status, plus exit_code/error when available. Status uses the same running/idle/closed vocabulary as execution and reads. Timestamps, output ranges and redundant booleans are no longer returned. Closed history remains discoverable for up to 30 minutes or replacement, allowing recovery after a lost response.

Optionally pass `command_id` to `session_read` to pin its status to that command; if a newer command has started, the read fails rather than mislabeling the result. Omit it to read retained session output by byte cursor across commands. Buffers retain the latest 1 MiB; dropping older requested output sets `truncated=true`, while pagination alone does not. Drain `has_more` even after command completion.

</details>

<details>
<summary>Session control: input, interrupt, EOF and stop</summary>

`session_control` requires session_id and action. Input targets only the active command; idle shells reject it, and new commands use execute_command. Only input requires/accepts input text, and only stop accepts optional wait_ms (default 1000). Input, interrupt and EOF share `{"ok":true}` after client acknowledgment, without claiming that a program consumed input or handled a signal. Stop returns status and optional exit_code/error, with no action echo. Input/signals may repeat side effects: do not blindly retry uncertain delivery.

Before interrupt or EOF, check `list_clients.client_info.session_controls` (`interrupt` and/or `eof`). An empty list means unavailable; older clients may need updating. Both operations wait up to 5 seconds for client acknowledgment. `ok=true` does not guarantee the program handled the operation; read output to verify. A timeout or cancellation leaves delivery uncertain, so do not blindly retry.

- `session_control(action="interrupt")` sends SIGINT to the current terminal foreground process group on Linux/macOS. An interactive shell may remain usable after its foreground command exits; the program may also ignore the signal or exit. This does not forcibly cancel the entire session like `session_control(action="stop")`. Windows pipe mode does not support interrupt.
- `session_control(action="eof")` follows previously queued input. On Linux/macOS canonical terminals it sends the configured VEOF character: pending input may be delivered first, requiring another EOF, and stdin is not permanently closed. Raw mode or disabled VEOF returns an error. On Windows pipes it permanently closes stdin while output remains readable; subsequent writes fail and repeated EOF is harmless. EOF at an idle shell prompt may exit the shell.

`session_control(action="stop")` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

| Status | Meaning and next step |
| --- | --- |
| `running` | Stop has been sent or remains pending. Continue reading state; remote termination is not confirmed. |
| `closed` | The session is no longer usable. Read retained output; a disconnection error means remote termination remains unconfirmed. |


Repeated stop does not resend pending cancellation and returns the same terminal state when already ended. Unknown/unauthorized sessions remain errors. Do not interpret closed as confirmed remote termination when error reports disconnection. Stop cannot undo completed operations, revoke credentials or stop other sessions.

</details>
