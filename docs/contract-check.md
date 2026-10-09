# External contract check

Run on 2026-10-06 against `bd` 1.3.1, `opencode` v2.0.22 and `herdr` 0.9.2, in a throwaway project at `/tmp/tsk-contract`.

| Check | Command | Observed | Pass |
|---|---|---|---|
| Beads init | `bd init --prefix tskc --skip-agents --skip-hooks --non-interactive` | Backend dolt, **embedded mode**, database `tskc`, stored under `/tmp/tsk-contract/.beads`. It did not touch the shared Dolt server at 127.0.0.1:3308. `bd create ... --silent` printed only the ID (`tskc-imo`). | PASS |
| Metadata set and listed | `bd update $ID --set-metadata tsk.session=ses_probe --set-metadata tsk.branch=task/probe`; `bd list --json --all -n 0 \| jq '.[0].metadata'` | `{"tsk.branch":"task/probe","tsk.session":"ses_probe"}`. Keys are flat dotted strings, and `-n 0` is accepted. | PASS |
| Claim | `bd update $ID --claim`; `bd show $ID --json \| jq '.[0]\|{status,assignee}'` | `{"status":"in_progress","assignee":"Antoine Gaudreau Simard"}` (assignee comes from the git user). | PASS |
| Unclaim | `bd update $ID --status open --assignee ""` | `{"status":"open","assignee":null}`. The assignee is `null`, not an empty string. | DEVIATES (`assignee` is `null`) |
| Claim conflict | `BEADS_ACTOR=another bd update $ID --claim` | exit 1. Stderr is 3 lines: `Error updating tskc-imo: issue already claimed by Antoine Gaudreau Simard`, `Error: 1 of 1 issues failed to update`, `  tskc-imo: updating issue: issue already claimed by Antoine Gaudreau Simard`. Stdout is empty. | PASS |
| Session create | `opencode api session.create -d '{"title":..,"location":{"directory":"/tmp/tsk-contract"},"metadata":{"tsk.task":"probe"}}'` | The operation-ID form works. `.data.id` is `ses_...`, `.data.location.directory` is `/tmp/tsk-contract`, and `.data.metadata` round-trips on `GET /api/session/{id}`. | PASS |
| Prompt starts work with no window | `opencode api POST /api/session/$SID/prompt -d '{"text":...}'` | Returns `{"data":{"id":"msg_...","sessionID":..,"type":"user","payload":{"text":...},"delivery":"steer"}}`. The events show `session.inbox.enqueued`, `session.inbox.delivered`, `session.execution.started`, `session.step.started` and reasoning deltas, so work does start headless. | PASS |
| Question → form (default model) | same prompt, then `GET /api/session/$SID/form` after 30 s | Count was `0`: the default model (`fledge-alpha-free`) returned a malformed tool call (`provider.invalid-output`: "OpenAI Chat tool call delta is missing id or name"), so the session failed with `session.execution.failed`. See the rerun rows below. | DEVIATES (default model fails; see rerun) |
| `GET /api/session/active` | `opencode api GET /api/session/active` | On the first call the CLI printed `Killed by SIGSEGV` (once). The retry returned `{"data":{"<sid>":{"type":"running"},...}}`, a map of session ID to `{type}`, not an array. | DEVIATES (returns a map, one SIGSEGV) |
| Question → form (rerun, explicit model) | `opencode api session.create -d '{...,"model":{"providerID":"anthropic","id":"claude-haiku-4-5"},...}'` in `/tmp/tsk-contract2`, then the same prompt, `GET /api/session/$SID/form` after 25 s | `Model.Ref` is `{providerID, id, variant?}`; the session reports `variant: "default"`. The form list had **1** entry: `{id: frm_..., sessionID, title: "Questions", metadata: {kind: "question", tool: {messageID, id}}, fields: [{key: "q0", title, description, type: "string", options: [{value,label,description}], custom: true}]}`. `form.created` arrived with `data.form.sessionID` = the session (the form object is under `data.form`). | PASS |
| Form cancel (rerun) | `opencode api DELETE /api/session/$SID/form/$FORM` | exit 0. Events: `form.cancelled` with `data: {id, sessionID}` (**`data.sessionID`, not `data.form.sessionID`**), then `session.tool.failed`, `session.step.failed` (`error.type` `aborted`, "Step interrupted"), `session.usage.updated` and `session.execution.interrupted` (`data.reason` `"shutdown"`). **No `session.idle` and no `session.execution.succeeded`.** The form list is `0` afterwards and `GET /api/session/active` no longer lists the session. | DEVIATES (ends with `session.execution.interrupted`, not idle/succeeded) |
| Event payloads (rerun) | `curl -sN -K - $URL/api/event`, filtered on `data.sessionID` or `data.form.sessionID` | Before cancel: `session.inbox.enqueued`, `session.inbox.delivered`, `session.instructions.updated`, `session.execution.started`, `session.step.started`, `session.reasoning.started`, `session.reasoning.ended`, `session.tool.input.started`, `session.tool.input.ended`, `session.tool.called`, `session.step.streamed`, `form.created`. After cancel: `form.cancelled`, `session.tool.failed`, `session.step.failed`, `session.usage.updated`, `session.execution.interrupted`. Envelope `{id, created, type, location, data, durable}` as assumed; `form.*` events carry no `durable` field. The first run's stream (default model) additionally showed `session.execution.failed` with `data.error`. | PASS for `form.*` and the envelope; `form.cancelled` carries `data.sessionID` |
| Normal completion | New session in `/tmp/tsk-contract3` with model `anthropic/claude-haiku-4-5`; subscribe to `/api/event`; one prompt: "Reply with the single word ok. Do not run any tools." | Events for the session, in order: `session.inbox.enqueued`, `session.execution.started`, `session.instructions.updated`, `session.inbox.delivered`, `session.step.started`, `session.reasoning.started/ended`, `session.text.started/delta/ended`, `session.step.streamed`, `session.step.ended` (`finish: "stop"`), `session.usage.updated`, **`session.execution.succeeded`** (`data.sessionID`). **No `session.idle` was emitted** (0 in the whole stream). The session was absent from `/api/session/active` afterwards. | DEVIATES (`session.execution.succeeded` only; no `session.idle`) |
| herdr workspace and tab create | `herdr workspace create --cwd .. --label tsk-contract --no-focus`; `herdr tab create --workspace $WS ...` | Both print JSON. The tab's `result.root_pane.pane_id` is `w9Y:p2` and `result.tab.tab_id` is `w9Y:t2`. The workspace's own root pane is `w9Y:p1`. | PASS |
| herdr `pane run` | brief form: `herdr pane run $PANE opencode --session $SID` | **Fails.** herdr's parser consumed `--session` as its own flag; the command ran against a herdr session socket and printed `{"error":{"code":"server_not_running",...}}`, exit 1. The `--` form (`pane run $PANE -- opencode --session $SID`) exits 0 but types the `--` literally into the pane's shell (`zsh: command not found: --`). **Working form:** pass the whole command as one quoted string: `herdr pane run "$PANE" "opencode --session $SID"`. It exits 0 and prints nothing (no JSON). | DEVIATES (command must be a single string) |
| herdr `agent list` shows session | `herdr agent list \| jq '.result.agents[] \| select(.agent_session.value==$s)'` | With the working form, after 10 s the pane appeared with `agent_session.value` = the session ID, `agent_session.source` `herdr:opencode`, `cwd` `/tmp/tsk-contract` and `agent_status` `idle`. `opencode --session <id>` attached to the API-created session (terminal title `OC \| tsk contract check`). | PASS |
| herdr `pane list` | `herdr pane list \| jq '.result.panes[]...'` | `{"pane_id":"w9Y:p2","workspace_id":"w9Y","cwd":"/tmp/tsk-contract","agent_status":"idle"}` | PASS |
| herdr `agent focus` | `herdr agent focus $PANE` | exit 0, prints JSON `{"id":"cli:agent:focus","result":{"agent":{...,"focused":true,...},"type":"agent_info"}}`. Before the agent registered it failed with `{"error":{"code":"agent_not_found",...}}`, exit 1. | PASS (`focused: true`; the visible tab switch was not observed) |
| Service status | `opencode service status; echo exit=$?` | Line 1 is the URL (`http://127.0.0.1:49374`), exit 0. | PASS |

## Surprises

1. **The default model failed; an explicit model works.** Sessions created without a `model` use the user's default model, which in the first run (`fledge-alpha-free`) failed with a malformed tool call, so tsk will inherit whatever model OpenCode defaults to. The rerun created the session with `model: {providerID: "anthropic", id: "claude-haiku-4-5"}` and the question/form chain worked.
2. **Cancelling a form interrupts the execution.** The cancel produced `session.execution.interrupted` (`reason: "shutdown"`), not `session.idle` or `session.execution.succeeded`. tsk must treat `session.execution.interrupted` as the end of a cancelled run, and read the session from `data.sessionID` on `form.cancelled` (`form.created` uses `data.form.sessionID`). Whether `session.idle` / `session.execution.succeeded` fire on a normally completed run was not exercised.
3. **`herdr pane run` command form.** Pass the command as a single string; unquoted arguments starting with `-` are parsed by herdr, and `--` is typed into the shell.
4. **`herdr pane run` prints nothing on success.** Exit 0 and empty output, and the pane needs several seconds before `agent list` reports it. `agent focus` before then returns `agent_not_found` (exit 1).
5. **Unclaim yields `assignee: null`**, not `""`.
6. **`bd init` uses embedded Dolt** in this version (database `tskc` inside the project's `.beads`), not the shared server at 127.0.0.1:3308. Nothing was added to the shared server.
7. **A single `Killed by SIGSEGV`** came from `opencode api GET /api/session/active`; the retry worked.

## Cleanup

The herdr workspace `w9Y` was closed and the OpenCode session `ses_eed1d5cd3ffe5xzN4Ii08cgdWt` was deleted (a later GET returns 404). The rerun session was deleted too (GET returns 404). No databases were dropped. The OpenCode service was not touched.

The herdr workspace and both OpenCode sessions were cleaned up as described above, and the third session (normal completion) was deleted too. `/tmp/tsk-contract`, `/tmp/tsk-contract2` and `/tmp/tsk-contract3` were left in place for manual removal.

## Spec deviations

Implementers must handle:

- `session.execution.interrupted` (`data.reason`, `data.sessionID`) ends a cancelled run; no `session.idle` or `session.execution.succeeded` follows a form cancel.
- A normally completed run ends with `session.execution.succeeded` (`data.sessionID`). `session.idle` was never emitted, so don't wait for it. A failed run ends with `session.execution.failed` (`data.error`).
- `form.created` carries the session in `data.form.sessionID`; `form.cancelled` carries it in `data.sessionID` (`data: {id, sessionID}`).
- `herdr pane run <pane> "<command>"` takes the command as one quoted string; unquoted flags are parsed by herdr, and `--` is typed into the shell. It prints nothing on success, and `agent list` and `agent focus` need several seconds before the pane registers.
- `GET /api/session/active` returns a map of session ID to `{type}`, not an array.
- `bd update --assignee ""` leaves `assignee: null`.
- Sessions created without `model` use OpenCode's default model, which failed in this run with a malformed tool call. tsk inherits whatever default OpenCode has, so it should surface `session.execution.failed`.

2026-10-07: tsk's own TUI (`tsk board`, `tsk project`) was removed in favour of the bt plugin (`tsk plugin`).

## Session linking (2026-10-09, bd 1.3.1, OpenCode 2.0.14)

| Check | Command | Observed | Result |
|---|---|---|---|
| bd show of a missing ID | `bd show tsk-zzzz --json` | Exit 1; stdout `{"error": "no issues found matching the provided IDs", "hint": ..., "schema_version": 1}`. Text that is not an ID gives the same answer. | PASS |
| herdr agent list fields | `herdr agent list` | Each agent has `agent`, `agent_session.{agent,kind,source,value}` (`source` `herdr:opencode`), `agent_status` (`idle`, `working`, `done`, ...), `cwd`, `pane_id`. | PASS |
