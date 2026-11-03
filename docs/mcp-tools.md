# MCP tool reference

Current source builds publish explicit output schemas and behavior annotations for all eight tools. Use the connected server's `tools/list` response as the contract; existing deployments and older release binaries may expose an earlier schema.

| Tool | When to use it | Main result fields |
| --- | --- | --- |
| `create_client` | Create a private connection invitation. | `client_token`, `invitation`, `expires_at`, platform commands, `display_text`. |
| `list_clients` | Inspect devices or wait for a connection. | `clients`; waiting calls also return `status`. |
| `p2p_transfer_start` | Bootstrap p2p-cli remotely and return Agent-side scripts after remote signaling registration. | `connection_id`, endpoint IDs, `state`, `shell_script`, `powershell_script`. |
| `execute_command` | Run a short non-interactive command and wait for its result. | `stdout`, `stderr`, `exit_code`, `timed_out`, `canceled`, `truncated`, optional `error`. |
| `session_start` | Start an interactive or long-running process. | `session_id`, initial `cursor=0`. |
| `session_read` | Read paginated output and check session completion. | `output`, `next_cursor`, `has_more`, `truncated`, `done`; terminal results add exit, timeout, cancellation and error fields. |
| `session_control` | Send input, interrupt, EOF or stop using `action`. | `action`; input returns `accepted`, interrupt/EOF return `delivered`, stop returns confirmation and terminal fields. |
| `session_list` | Rediscover this token's retained sessions, optionally filtered by `client_id`, even for offline devices. | `sessions`: session, device, original command, timestamps, lifecycle status and retained output cursor range. |


## Common usage

Create an invitation and share its connection command privately. Once the device connects, execute commands. For interactive work or persistent shell state, start a session and send input:

```json
{"session_id":"…","action":"input","input":"pwd\n"}
```

Use the connected server’s `tools/list` as the source of truth for tools and fields.

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
<summary>Commands and interactive sessions</summary>

Each `execute_command` starts a fresh shell. Set `cwd` and `env` explicitly; they do not persist across calls. Its timeout defaults to 60 seconds, with `timeout_ms=0` disabling it. Captured stdout/stderr share a 1 MiB limit. Use session tools for prompts, incremental output or long jobs, and retain the `session_id` and the byte-based `next_cursor`. Session buffers retain the latest 1 MiB; inspect `truncated` to detect missing older output.

To preserve working directory, variables, functions or an activated environment, explicitly launch a persistent shell with `session_start` (for example, `bash -i` where installed on Linux/macOS), then send commands through `session_control(action="input")` using the same `session_id`. Each `session_start` creates an independent process; a one-shot command leaves no persistent shell behind. Client disconnection ends sessions; reconnect does not restore shell state.

</details>

<details>
<summary>Reading output and finding sessions</summary>

`session_read.max_bytes` defaults to 16384, with a range of 4–65536 bytes. Output never splits a UTF-8 character. Continue from each returned `next_cursor`. `has_more=true` means retained output remains; even with `done=true`, keep reading until `has_more=false`. `done` and `exit_code` describe the whole session process, not individual commands in a persistent shell. Pagination alone does not set `truncated`.

`session_list` returns this token's sessions in start-time order, including retained results for offline devices. `output_start_cursor` points to the earliest retained output; `output_end_cursor` points after the latest output, so reading from it skips existing output. Session `status` is `running`, `stopping` or `finished`; it does not infer shell idleness, current directory or individual command state. Finished records are retained for up to 30 minutes and may be evicted when new sessions start.

</details>

<details>
<summary>Session control: input, interrupt, EOF and stop</summary>

`session_control` requires `session_id` and `action`. Only `action=input` accepts and requires `input`; only `action=stop` accepts optional `wait_ms` (default 1000). Unrelated parameters are rejected. Every successful result includes its `action`, with separate result schemas for sent input, acknowledged control and stop confirmation. The tool is conservatively marked non-idempotent because input and interruption can repeat side effects; repeated stop remains safe as described below. Management access exposes eight tools; device-scoped access exposes seven, excluding `create_client`.

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
