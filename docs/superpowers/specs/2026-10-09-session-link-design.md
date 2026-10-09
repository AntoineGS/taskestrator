# Linking agent sessions to tasks claimed outside dispatch

Date: 2026-10-09
Status: implemented on feat/session-link
Task: `tsk-jqp`

## Purpose

tsk follows a task's agent through one piece of data: the `tsk.session`
metadata on the bead. `D` writes it during dispatch. A task an agent claims by
itself, for example with `bd update <id> --claim` from an OpenCode session the
user started by hand in a herdr pane, never gets it. tsk then derives the
`untracked` state and pushes nothing, so bt shows no live state and offers no
jump, even though herdr already knows which pane runs that session.

The agent cannot write the link itself: OpenCode v2 does not expose the session
ID to shell commands (the shell environment is the service's or the session's
environment plus `TERM` and `OPENCODE_TERMINAL`), and commands run under the
background service, so the process tree does not lead to the TUI either.

Success means:

- An agent that claims a task from any OpenCode session, without being told
  anything, shows its live state in bt within seconds of the claim, and `J`
  focuses its pane.
- A task claimed before this existed, or claimed in a way tsk did not see, can
  be linked to one of the OpenCode panes in its checkout from bt with one key
  and one choice.
- A link never replaces a session that is still alive.

## Decisions

| Topic | Decision |
|---|---|
| Source of truth | Unchanged: `tsk.session` (root session) and `tsk.worktree` (the directory a reopened tab starts in). Once they are written, the existing engine, jump and re-dispatch code apply as for a dispatched task. `tsk.branch` is not written. |
| Automatic link | An OpenCode v2 plugin hooks `tool.execute.after` on `shell` calls and hands claim-looking commands to `tsk link`. No agent instructions are involved. |
| Where the logic lives | In Go (`internal/link`), so it is covered by `go test`. The plugin only pre-filters and starts `tsk link`. |
| What counts as linked | Whatever actually happened: a task is linked only if `bd show` reports it `in_progress`. A failed claim or a misparsed command does nothing. |
| Manual link | A bt plugin action `link` (key `K`) on in-progress tasks with no session or a lost one. The user picks an OpenCode pane in the task's checkout. |
| Replacement | A link never replaces a session that still exists in OpenCode. A session OpenCode no longer has is replaced. |
| bt | No change. The protocol already has `ui.select`. |

## Non-goals

- Claims made through an MCP beads tool or by a script that builds the `bd`
  command; the manual action covers them.
- Telling the agent its session ID, or any agent instructions.
- Linking from the `bd` side (hooks).
- Unlinking. Closing the task or re-dispatching it already moves on.
- Linking sessions that run outside herdr from bt (they can still be linked
  automatically; jump then reopens a tab).

## The link rule

`internal/link` applies one rule for a task ID, a directory and an OpenCode
session ID. The automatic and manual paths both use it.

1. Resolve the root session with `opencode.Client.Root`, so a claim made by a
   subagent links the session the pane shows. Fetch the root session to read
   its `location.directory`.
2. `bd show <id> --json` in the directory. A task that is not found, or whose
   status is not `in_progress`, is skipped.
3. If the task's `tsk.session` is the root session, nothing to do.
4. If it names another session: `GET /api/session/<id>`. Found means alive and
   the task is skipped; `404` means gone and the link goes ahead; any other
   error is an error.
5. `bd update <id> --set-metadata tsk.session=<root>
   --set-metadata tsk.worktree=<root session directory>`.

Each decision returns a result (`linked`, `same`, `not_found`, `not_in_progress`,
`live_session`) or an error, for logging and for the action's toast.

## Automatic link

### `tsk link`

```
tsk link --session <id> [--workdir <dir>] --command <command>
```

- The base directory is the session's own `location.directory` (the
  directory OpenCode runs its shell commands in), fetched from OpenCode.
  `--workdir` is the shell call's `workdir`: absolute, it replaces the base;
  relative, it is joined to it. The command is parsed once before connecting,
  so a command with no claim exits 0 without contacting OpenCode.
- Splits the command into segments on `&&`, `||`, `;`, `|` and newlines,
  outside single quotes, double quotes and backslash escapes.
- A segment `cd <path>` changes the directory used for the segments after it
  (relative paths resolve against the current one).
- A segment is a claim when, after leading `NAME=value` assignments, its
  command's base name is `bd`, its first word that does not start with `-` is
  `update`, and it contains `--claim`, `--status in_progress`,
  `--status=in_progress` or `-s in_progress`.
- bd's global flags before `update` are understood: `-C`/`--directory`
  changes the segment's directory like `cd`; the values of `--actor`,
  `--dolt-auto-commit` and `--mem-profile` are skipped; a segment with `--db`,
  `--database` or `--global` targets another database and is ignored (the
  manual action covers it).
- Candidate IDs are the words after `update` that do not start with `-` and
  match `^[A-Za-z0-9][A-Za-z0-9_]*-[A-Za-z0-9._]+$`. A flag value that looks like
  an ID is harmless: step 2 of the link rule does not find it.
- Each candidate goes through the link rule in its segment's directory, once
  per (directory, ID).
- Writes one line per decision to `~/.local/state/tsk/link.log`
  (`$XDG_STATE_HOME/tsk/link.log` when set): time, session, directory, ID and
  result or error. Never logs the OpenCode password.
- Exits 0 when every candidate got a result, 1 when any got an error (for
  example OpenCode unreachable or `bd` failing), 2 on bad arguments.

The OpenCode client is built the same way `tsk plugin` builds it for dispatch.

### OpenCode plugin

`opencode/tsk-link.js`, plain JS with no build step, installed by symlinking it
into `~/.config/opencode/plugins/`:

```js
export default { id: "tsk.link", async setup(ctx) { /* ... */ } }
```

- Registers `ctx.tool.hook("execute.after", ...)`.
- Acts only when `tool === "shell"`, `status === "completed"`, `input.command`
  is a string and it matches the pre-filter: it contains the words `bd` and
  `update`, and `--claim` or `in_progress`.
- Starts `tsk link --session <sessionID> --command <command>`, plus
  `--workdir <input.workdir>` when the call has one, detached with stdio
  ignored, and does not wait. The plugin does not resolve directories. Spawn errors (no `tsk` on
  PATH) are swallowed. The hook never throws and never delays the tool call.
- The pure parts (pre-filter, arguments) are exported for the test.

A `shell` call with `background: true` can finish after the hook fires; the
claim may not have happened yet, step 2 skips it, and the manual action covers
it.

## Manual link

### State

A bead whose derived state is `untracked` and whose status is `in_progress` is
pushed with `tsk.state = untracked`. The manifest gains the value
`{ID: "untracked", Label: "Not linked", Lane: false}`, with no badge, like
`queued`. Other untracked beads (status `blocked`, for example) are still
cleared. This replaces the "untracked: `state.clear`" row of the state mapping
in `2026-10-07-bt-plugins-design.md`.

### Action

The manifest gains `{ID: "link", Label: "Link agent session", Key: "K"}`. `L`
is bound in bt's list and board views; `K` is bound only in bt's history view,
which does not dispatch plugin keys.

`link` is offered when herdr is running and the bead's checkout is known, on:

- `untracked` beads (actions `[link]`);
- `lost` beads, after `dispatch` (actions `[dispatch, link]`), for an agent
  restarted by hand in a new session.

On invoke:

1. Refuse unless the bead is still `untracked` or `lost` (`not_linkable`), and
   use the existing per-bead in-flight guard.
2. `herdr agent list` through a new `herdr.Agents` method returning each pane's
   ID, directory, session ID, session source and status. Keep panes whose
   source is `herdr:opencode`, that have a session ID, and whose directory is
   the checkout or inside it (worktrees under `.worktrees/` included).
3. None: fail with `no_sessions` ("no OpenCode pane in `<repo>`; start one in
   herdr or claim the task from an agent").
4. For each pane, fetch the session title. A pane whose session cannot be
   fetched is left out (if that leaves none, `no_sessions`). Ask with
   `ui.select`, even for a single option: label the session title, description
   the directory relative to the checkout and the pane status, value the
   session ID. Dismissed: return with no toast and no change.
5. Apply the link rule in the checkout. `linked` or `same`: toast "Linked
   `<id>` to `<title>`" and `Loop.Refresh(root)`, as after a dispatch. Any
   other result: fail with `not_linkable` naming it. Errors: `check_failed`.

## Error handling

- The OpenCode plugin never fails or delays a tool call. Every automatic-link
  outcome is in `link.log`.
- `tsk link` errors leave the task unlinked; the manual action is the fallback.
- New action error types, added to the README table: `no_sessions`,
  `not_linkable`. herdr down and OpenCode unreachable reuse `no_herdr` and
  `check_failed`.
- No secret is logged, sent in a toast or written to a section.

## Testing

Go (`make test`):

- `internal/link` parsing: table of commands for chains, quotes, `cd`,
  `VAR=... bd`, `/usr/bin/bd`, `--status=in_progress`, `-s in_progress`,
  several IDs, dotted IDs, ID-looking flag values, and non-claims (`bd show`,
  `bd update x --notes "claim"`, `echo bd update x --claim`, `bd update x -s
  open`).
- `internal/link` rule, with fake tracker and OpenCode: child session links the
  root; not found and not in progress are skipped; same session; live other
  session skipped; gone session replaced; OpenCode error; one log line per
  decision.
- `internal/herdr`: `Agents` parsed from recorded `agent list` JSON.
- `internal/btplugin`: `untracked` pushed only for `in_progress`; `link`
  offered on untracked and lost beads only with herdr up and a known checkout;
  manifest declares the value and action; the action's pane filter, option
  labels, dismissal, `no_sessions`, `not_linkable`, a successful link calling
  `Refresh`, and the in-flight guard.

JS: `opencode/tsk-link.test.mjs` with `node --test`, no dependencies: the
pre-filter, the arguments, and that non-`shell`, failed and
non-matching calls start nothing. `make test` runs it after `go test`.

Against the real tools, recorded in `docs/contract-check.md`:

- the `execute.after` event for `shell`: `tool`, `sessionID`, `status`,
  `input.command`, `input.workdir`;
- that a plugin in `~/.config/opencode/plugins/` sees `shell` calls of every
  session, including sessions started through the API;
- `herdr agent list` fields `agent`, `agent_session.source`,
  `agent_session.value`, `agent_status` and `cwd` (checked 2026-10-09).
- `bd show <missing> --json` exits 1 and prints
  `{"error": "no issues found matching the provided IDs", ...}` on stdout
  (bd 1.3.1, checked 2026-10-09).

One manual end-to-end run: a hand-started OpenCode pane in herdr claims a
throwaway task; bt shows its live state and `J` focuses the pane; `K` links a
second task claimed before the plugin was installed.

## Docs

- README: installing the OpenCode plugin, the `K` action, the "Not linked"
  state, the new error types, `link.log`.
- `cmd/tsk/main.go` usage: a `tsk link` line.
- `2026-10-07-bt-plugins-design.md`: the state mapping row for untracked
  in-progress beads, and `link` under Actions.
