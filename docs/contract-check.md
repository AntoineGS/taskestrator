# External contract check

Run on 2026-10-06 against `bd` 1.3.1, `opencode` v2.0.22 and `herdr` 0.9.2, in a throwaway project at `/tmp/tsk-contract`.

| Check | Command | Observed | Pass |
|---|---|---|---|
| Beads init | `bd init --prefix tskc --skip-agents --skip-hooks --non-interactive` | Backend dolt, **embedded mode**, database `tskc`, stored under `/tmp/tsk-contract/.beads`. It did not touch the shared Dolt server at 127.0.0.1:3308. `bd create ... --silent` printed only the ID (`tskc-imo`). | PASS |
| Metadata set and listed | `bd update $ID --set-metadata tsk.session=ses_probe --set-metadata tsk.branch=task/probe`; `bd list --json --all -n 0 \| jq '.[0].metadata'` | `{"tsk.branch":"task/probe","tsk.session":"ses_probe"}`. Keys are flat dotted strings, and `-n 0` is accepted. | PASS |
| Claim | `bd update $ID --claim`; `bd show $ID --json \| jq '.[0]\|{status,assignee}'` | `{"status":"in_progress","assignee":"Antoine Gaudreau Simard"}` (assignee comes from the git user). | PASS |
| Unclaim | `bd update $ID --status open --assignee ""` | `{"status":"open","assignee":null}`. The assignee is `null`, not an empty string. | PASS |
| Claim conflict | `BEADS_ACTOR=another bd update $ID --claim` | exit 1. Stderr is 3 lines: `Error updating tskc-imo: issue already claimed by Antoine Gaudreau Simard`, `Error: 1 of 1 issues failed to update`, `  tskc-imo: updating issue: issue already claimed by Antoine Gaudreau Simard`. Stdout is empty. | PASS |
| Session create | `opencode api session.create -d '{"title":..,"location":{"directory":"/tmp/tsk-contract"},"metadata":{"tsk.task":"probe"}}'` | The operation-ID form works. `.data.id` is `ses_...`, `.data.location.directory` is `/tmp/tsk-contract`, and `.data.metadata` round-trips on `GET /api/session/{id}`. | PASS |
| Prompt starts work with no window | `opencode api POST /api/session/$SID/prompt -d '{"text":...}'` | Returns `{"data":{"id":"msg_...","sessionID":..,"type":"user","payload":{"text":...},"delivery":"steer"}}`. The events show `session.inbox.enqueued`, `session.inbox.delivered`, `session.execution.started`, `session.step.started` and reasoning deltas, so work does start headless. | PASS |
| Question → form | same prompt, then `GET /api/session/$SID/form` after 30 s | **Not exercised.** Count was `0`. The step failed with `session.step.failed` and `session.execution.failed`, error type `provider.invalid-output`: "OpenAI Chat tool call delta is missing id or name" (provider response from model `fledge-alpha-free`, `finish_reason: tool_calls` with no tool call). The model's malformed tool call stopped the question tool from being invoked. Only one prompt was approved, so it was not retried. | **FAIL (inconclusive)** |
| `GET /api/session/active` | `opencode api GET /api/session/active` | On the first call the CLI printed `Killed by SIGSEGV` (once). The retry returned `{"data":{"<sid>":{"type":"running"},...}}`, a map of session ID to `{type}`, not an array. | PASS, with a flake |
| Form cancel via `DELETE /api/session/$SID/form/$FORM` | n/a | Not run: there was no form. | **FAIL (not exercised)** |
| Event payloads | `curl -sN -K - $URL/api/event` into a file | The stream carries `data:` lines with `{id, created, type, location, data, durable}`. Types seen for our session: `session.created`, `session.inbox.enqueued`, `session.inbox.delivered`, `session.instructions.updated`, `session.execution.started`, `session.step.started`, `session.reasoning.started/delta/ended`, `session.step.streamed`, `session.step.failed`, `session.execution.failed`. Session-scoped events carry `data.sessionID`. Global events have no session (`mcp.status.changed`, `command.updated`, etc.). **Not seen:** `form.created`, `form.cancelled`, `session.idle`, `session.execution.succeeded`. The failure carries `data.error.{type,message,status,response}`. | **FAIL (inconclusive)** for the form and idle events; the rest PASS |
| herdr workspace and tab create | `herdr workspace create --cwd .. --label tsk-contract --no-focus`; `herdr tab create --workspace $WS ...` | Both print JSON. The tab's `result.root_pane.pane_id` is `w9Y:p2` and `result.tab.tab_id` is `w9Y:t2`. The workspace's own root pane is `w9Y:p1`. | PASS |
| herdr `pane run` | brief form: `herdr pane run $PANE opencode --session $SID` | **Fails.** herdr's parser consumed `--session` as its own flag; the command ran against a herdr session socket and printed `{"error":{"code":"server_not_running",...}}`, exit 1. The `--` form (`pane run $PANE -- opencode --session $SID`) exits 0 but types the `--` literally into the pane's shell (`zsh: command not found: --`). **Working form:** pass the whole command as one quoted string: `herdr pane run "$PANE" "opencode --session $SID"`. It exits 0 and prints nothing (no JSON). | PASS with a corrected form |
| herdr `agent list` shows session | `herdr agent list \| jq '.result.agents[] \| select(.agent_session.value==$s)'` | With the working form, after 10 s the pane appeared with `agent_session.value` = the session ID, `agent_session.source` `herdr:opencode`, `cwd` `/tmp/tsk-contract` and `agent_status` `idle`. `opencode --session <id>` attached to the API-created session (terminal title `OC \| tsk contract check`). | PASS |
| herdr `pane list` | `herdr pane list \| jq '.result.panes[]...'` | `{"pane_id":"w9Y:p2","workspace_id":"w9Y","cwd":"/tmp/tsk-contract","agent_status":"idle"}` | PASS |
| herdr `agent focus` | `herdr agent focus $PANE` | exit 0, prints JSON `{"id":"cli:agent:focus","result":{"agent":{...,"focused":true,...},"type":"agent_info"}}`. Before the agent registered it failed with `{"error":{"code":"agent_not_found",...}}`, exit 1. | PASS |
| Service status | `opencode service status; echo exit=$?` | Line 1 is the URL (`http://127.0.0.1:49374`), exit 0. | PASS |

## Surprises

1. **The prompt failed at the provider.** The default model returned a malformed tool-call chunk, so the question tool was never called. This leaves the `form.created`, form-list, form-cancel and `session.idle` / `session.execution.succeeded` assumptions unverified. It's a provider problem, not a contract contradiction, but the first-implementation-task check is still open. It needs a rerun with a reliable model (not approved here, as only one prompt was allowed).
2. **`herdr pane run` command form.** Pass the command as a single string; unquoted arguments starting with `-` are parsed by herdr, and `--` is typed into the shell.
3. **`herdr pane run` prints nothing on success.** Exit 0 and empty output, and the pane needs several seconds before `agent list` reports it. `agent focus` before then returns `agent_not_found` (exit 1).
4. **Unclaim yields `assignee: null`**, not `""`.
5. **`bd init` uses embedded Dolt** in this version (database `tskc` inside the project's `.beads`), not the shared server at 127.0.0.1:3308. Nothing was added to the shared server.
6. **A single `Killed by SIGSEGV`** came from `opencode api GET /api/session/active`; the retry worked.
7. **`/tmp/tsk-contract` was not removed.** The cleanup `rm -rf` was blocked by the CC Safety Net hook because the directory contains `.git`. Left in place for the controller or user to remove.

## Cleanup

The herdr workspace `w9Y` was closed and the OpenCode session `ses_eed1d5cd3ffe5xzN4Ii08cgdWt` was deleted (a later GET returns 404). No databases were dropped. The OpenCode service was not touched.
