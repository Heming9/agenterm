# MCP tool reference

Current source builds publish explicit output schemas and behavior annotations for all seven tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_clients` | Inspect devices or wait for a connection. | `clients`; waiting calls also return `status`. |
| `p2p_transfer_start` | Bootstrap p2p-cli remotely and return Agent-side scripts after remote signaling registration. | `connection_id`, endpoint IDs, `state`, `shell_script`, `powershell_script`. |
| `execute_command` | Create through setup or reuse through session_id; anonymous terminals close on fast completion or are retained on wait expiry. | `session_id`, `command_id`, `session_created`, `status`, `output`, `next_cursor`, `command_done`, `session_alive`, and completion fields. |
| `session_read` | Read paginated output and inspect command completion separately from terminal lifetime. | `output`, `next_cursor`, `has_more`, `truncated`, `command_done`, `session_alive`, `done`, and completion fields. |
| `session_control` | Send input, interrupt, EOF or stop using `action`. | `action`; input returns `accepted`, interrupt/EOF return `delivered`, stop returns confirmation and terminal fields. |
| `session_list` | Rediscover this token's retained sessions, optionally filtered by `client_id`, even for offline devices. | `sessions`: session, device, latest command, timestamps, lifecycle status and retained output cursor range. |


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

`list_clients` returns `status`, `client_id`, `connected_at`, and a `client_info` object for each device. The device `status` displays `Connected`, with the latest server-client heartbeat RTT when available, such as `Connected · 203ms`. `client_info` contains `hostname`, `platform`, `arch` (client process architecture), `shell` (the interpreter path used for commands and sessions), `default_cwd` (the client working directory inherited when `cwd` is omitted), and `client_version` (the client application version reported during the handshake). Empty metadata fields mean unknown or not reported by the client. Both server and client must be updated to expose the new metadata.

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

Top-level `execute_command.session_id` only reuses an existing terminal and rejects unknown/closed IDs. `setup` always creates a terminal and contains optional `cwd`, `env` and `session_id`; an existing setup ID is rejected, never overwritten or reused. setup and top-level session_id cannot be combined. An explicit setup ID creates a persistent terminal; omit that ID for a generated anonymous terminal. Omitting both also creates an anonymous terminal: fast completion closes it; wait expiry retains the same shell. IDs are unique within a token; multi-device creation requires client_id, while existing IDs identify their devices. session_created=true means creation; false means reuse.

Directory, variables, functions and environment activation persist within the shell. `cwd`/`env` are available only inside setup to initialize new terminals. Reuse without setup; change state with cd/export or shell equivalents. Each terminal accepts one command at a time and returns session_busy when occupied, without queueing or opening a replacement.

`wait_ms` defaults to 10000 (10 seconds), range 0–20000. It begins at client admission and includes shell startup for a new anonymous terminal. Setting 0 retains an anonymous terminal immediately; the call still waits for client admission. Waiting expiry never kills execution. Fast results report `status=completed`; running results include available output and `next_cursor`. Read with `session_read` and interact with `session_control`. `persistent=true` means command completion will leave the shell alive; `promoted=true` identifies an anonymous terminal retained after its initial wait (including 0). Named and promoted terminals do not expire when idle; close them with `session_control(action=stop)`. Canceling an MCP waiter does not cancel accepted work or its client-owned retention timer. `timeout_ms` is no longer supported.

Output is a combined terminal `output` stream. `command_done`/`exit_code` describe the latest command; `done` means the terminal ended; `session_alive` indicates availability. A fast anonymous command returns `command_done=true`, `done=true`, `session_alive=false`, `persistent=false`, `promoted=false`. A completed named or promoted command returns `done=false`, `session_alive=true`, `persistent=true`. Client shell integration reports command boundaries and exit codes without guessing visible prompts.

Linux/macOS use the reported shell and a PTY. Windows uses PowerShell and pipes for basic input interaction without full terminal emulation. Persistent terminals inherit client environment but do not load user shell startup files, so the integration remains reliable. Prompt hooks/PS1 are reserved; do not override them or terminal echo settings. Initialize additional environments explicitly in commands. Both server and client must be updated; older clients receive an upgrade error.

Each client process permits at most 10 live terminals, including starting, running, idle and closing shells until the OS process exits. CLI/GUI share the same client execution layer and quota; multiple connections within a process share the limit. Other processes using the same token have independent limits. At capacity, anonymous creation or a missing named terminal returns a terminal-limit error. Explicitly selecting an existing terminal by session_id still permits reuse; another terminal is never chosen automatically. Closed output history consumes no slots. The server applies only output-cache retention, never terminal quotas or idle cancellation. Disconnect terminates the connection’s terminals; reconnect loses shell state. Restarting the server frontend preserves runtime-owned connections and terminals.

</details>

<details>
<summary>Reading output and finding sessions</summary>

`session_read.max_bytes` defaults to 16384, with a range of 4–65536 bytes. Output never splits a UTF-8 character. Continue from each returned `next_cursor`. `has_more=true` means retained output remains; even with `done=true`, keep reading until `has_more=false`. `command_done` and `exit_code` describe the latest command; `done` only indicates terminal termination. Pagination alone does not set `truncated`.

`session_list` returns terminals in creation order, including retained records for offline devices. Filter by `client_id`; `session_id`, `persistent` and `promoted` describe client-reported retention. The old `default` flag has been removed. `status` is `idle`, `running`, `stopping` or `finished`. `command`/`command_id` identify the latest submitted command, `command_done` means command completion, and `done` means terminal termination. `output_start_cursor` is the earliest retained offset and `output_end_cursor` the end. Ended records last up to 30 minutes or until replaced by newer terminals.

Optionally pass `command_id` to `session_read` to pin its status to that command; if a newer command has started, the read fails rather than mislabeling the result. Omit it to read retained session output by byte cursor across commands. Buffers retain the latest 1 MiB; dropping older requested output sets `truncated=true`, while pagination alone does not. Drain `has_more` even after command completion.

</details>

<details>
<summary>Session control: input, interrupt, EOF and stop</summary>

`session_control` requires `session_id` and `action`. Input is only accepted by an active command; idle terminals reject it. Submit subsequent commands through `execute_command`. Only `action=input` accepts and requires `input`; only `action=stop` accepts optional `wait_ms` (default 1000). Unrelated parameters are rejected. Every successful result includes its `action`, with separate result schemas for sent input, acknowledged control and stop confirmation. The tool is conservatively marked non-idempotent because input and interruption can repeat side effects; repeated stop remains safe as described below. Management access exposes seven tools; device-scoped access exposes six, excluding `create_client`.

Before interrupt or EOF, check `list_clients.client_info.session_controls` (`interrupt` and/or `eof`). An empty list means unavailable; older clients may need updating. Both operations wait up to 5 seconds for client acknowledgment. `delivered=true` does not guarantee the program handled the operation; read output to verify. A timeout or cancellation leaves delivery uncertain, so do not blindly retry.

- `session_control(action="interrupt")` sends SIGINT to the current terminal foreground process group on Linux/macOS. An interactive shell may remain usable after its foreground command exits; the program may also ignore the signal or exit. This does not forcibly cancel the entire session like `session_control(action="stop")`. Windows pipe mode does not support interrupt.
- `session_control(action="eof")` follows previously queued input. On Linux/macOS canonical terminals it sends the configured VEOF character: pending input may be delivered first, requiring another EOF, and stdin is not permanently closed. Raw mode or disabled VEOF returns an error. On Windows pipes it permanently closes stdin while output remains readable; subsequent writes fail and repeated EOF is harmless. EOF at an idle shell prompt may exit the shell.

`session_control(action="stop")` waits up to `wait_ms` milliseconds for a terminal result (default 1000, range 0–20000). Setting 0 returns immediately. Interpret its result as follows:

| Status | Meaning and next step |
| --- | --- |
| `stopping` | Request sent or already pending, but termination is not confirmed. Continue `session_read` until `done=true`. |
| `stopped` | The target reported a terminal result with `canceled=true`. Read any remaining session output. |
| `finished` | The session ended for another reason. Inspect `exit_code`, `timed_out`, `canceled` and `error`; a disconnect does not confirm a remote cancellation. |

`accepted=true` alone never means the process has stopped. Repeating a stop while cancellation is pending does not send it again. A session already ended before the call returns `already_done=true` and `accepted=false` without an error; unknown or unauthorized sessions are errors. Stopping does not undo completed operations, revoke the device credential or stop other sessions. Do not blindly retry commands or input writes: they can repeat side effects.

</details>
