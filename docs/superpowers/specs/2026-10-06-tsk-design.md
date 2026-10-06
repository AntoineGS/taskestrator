# tsk: task-centric dispatch and status for Beads and OpenCode

Date: 2026-10-06
Status: draft, awaiting review

## Purpose

A solo developer runs OpenCode agents across many repositories, often several
per repository. The goal is to assign a task and have an agent start on it with
one key, and to see in one place which tasks are running, which are waiting on
the developer, and which are finished. The view is organised by task, not by
agent or terminal pane.

Success means:

- One keybind opens a modal listing dispatched tasks from every project, each
  with its live state.
- From a project, one key dispatches a ready task into its own worktree and
  herdr tab, with an OpenCode agent already working on it.
- A task always knows its OpenCode session, so a closed session can be
  reopened from the task.
- Orchestration itself spends no LLM tokens.

## Decisions

| Topic | Decision |
|---|---|
| Task store | Beads (`bd`) on the shared Dolt server, one database per project. |
| Live state source | The OpenCode v2 background service (HTTP API and event stream), not herdr. |
| Dispatch surface | herdr: one tab per task in the project's workspace. |
| Agent | OpenCode only in the first version. |
| Process model | One binary, no background process. Live state is derived when a view opens and followed while it stays open. |
| "Complete" signal | The agent sets the task to the `review` status with one `bd` command. An idle session whose task is not marked counts as waiting on the user. |
| Isolation | One git worktree and branch per task. |
| Project discovery | Scan configured roots for `.beads/metadata.json`. |
| Board actions | Enter jumps to the task's tab, reopening it if closed. |
| beads_viewer | Not used as a base: it has no extension point, reads JSONL exports rather than Dolt, works per repository, and its license rider grants no rights to Anthropic, OpenAI or anyone acting for them. |

## Non-goals for the first version

- Alerts while no view is open.
- Answering questions or permissions from the board.
- Cleaning up merged worktrees and branches.
- Choosing a model or agent per task.
- Claude Code and Codex.
- Any automatic push, merge or rebase.

## Commands

- `tsk board`: the global modal. Intended to run in a herdr popup.
- `tsk project [dir]`: the task list and dispatch for one project. Defaults to
  the project containing the current directory.

## Components

| Package | Responsibility | Depends on |
|---|---|---|
| `discover` | Scans the configured roots for `.beads/metadata.json` and returns each project's Beads database name, issue prefix and repo path. | filesystem |
| `tracker` | The only caller of `bd`, through `bd -C <repo> … --json`: list tasks, claim, set status, read and merge task metadata. | `bd` |
| `opencode` | HTTP client for the background service: sessions, active sessions, pending forms and permissions, session creation, prompting, and the event stream. Reads the service URL and password from OpenCode's own config and never logs the password. | OpenCode v2 API |
| `herdr` | Finds or creates a project's workspace by directory, opens a named tab running a command, and focuses the pane whose `agent_session` matches a session ID. | `herdr` |
| `status` | A pure function from a task and a session snapshot to a display state. No I/O. | none |
| `dispatch` | Runs the dispatch steps against the `tracker`, `opencode` and `herdr` interfaces and undoes completed steps on failure. | the three adapters |
| `ui` | Bubble Tea views for the board and the project list. | all of the above |

`tracker`, `opencode` and `herdr` are interfaces so that `dispatch`, `status` and
`ui` can be tested against fakes.

## Data

Stored on each Beads task as metadata, written once at dispatch:

| Key | Value |
|---|---|
| `tsk.session` | OpenCode session ID |
| `tsk.worktree` | Absolute worktree path |
| `tsk.branch` | Branch name, `task/<id>` |

Metadata writes merge with existing keys rather than replacing them.

Beads statuses used:

- `open`: not dispatched.
- `in_progress`: set by the claim at dispatch.
- `review`: a custom status in the `wip` category, set by the agent when its
  work is ready for review. It stays out of `bd ready` and appears in
  `bd list`.
- `closed`: set by the developer after review and merge.

Live state (running, waiting on a question or permission, idle) is never
stored. It is read from OpenCode each time a view opens and kept current from
the event stream.

## Status rules

The `status` function applies these rules in order; the first match wins.

| # | Condition | State |
|---|---|---|
| 1 | Task is `closed` | done |
| 2 | Task is `review` | review |
| 3 | Task has no `tsk.session` and is `open` | queued |
| 3a | Task has no `tsk.session` and any other status (claimed or blocked outside tsk) | untracked, shown with its Beads status; never dispatchable |
| 4 | OpenCode has no session with that ID | lost |
| 5 | Session has a pending form or permission | waiting on you (question or permission) |
| 6 | Session is running | in progress |
| 7 | Otherwise (session idle) | waiting on you (idle) |

If the OpenCode service is unreachable, every dispatched task that is not
`closed` or `review` is shown as unknown and the view shows a banner.

## Board

Lists dispatched, non-closed tasks from every discovered project, grouped in
this order: waiting on you, in progress, review, lost. Each row shows the
project, task ID, title, waiting label and time in the current state. The
header shows the number of queued tasks per project.

Keys: `j`/`k` move, `/` filters, Enter jumps, `a` toggles queued tasks and
tasks closed in the last 24 hours, `q` or Esc closes.

Jumping focuses the herdr pane whose `agent_session` matches the task's
session. If there is none, it opens a tab in the task's worktree running
`opencode --session <id>`. On a lost task, Enter explains that it can be
dispatched again from `tsk project`, where `d` works on lost tasks as well as
ready ones.

Questions and permissions raised by a subagent arrive on a child session. The
board follows `parentID` links to the task's session, and a task's snapshot
counts pending forms and permissions across its whole session tree.

Keeping current while open:

- OpenCode events for session status, execution start and end, idle, and forms
  and permissions being created or resolved update that session's snapshot.
- Dispatched tasks are re-read from Beads when one of their sessions goes idle
  and every 10 seconds, because the agent's `bd` writes do not appear as
  OpenCode events.
- On startup and after an event-stream reconnect, the full state is reloaded.

## Project view and dispatch

Lists the project's tasks with the same states, ready tasks first in
`bd ready` order. It refreshes on the re-read interval rather than following
the event stream. Enter on a dispatched task jumps to it. `d` on a ready or
lost task dispatches it:

1. Claim with `bd update <id> --claim`. Stop if another actor holds it.
2. Create `.worktrees/<id>` on branch `task/<id>` from the default branch, or
   reuse it if it exists. The default branch is the target of
   `refs/remotes/origin/HEAD`, falling back to `main`. `.worktrees/` must be
   git-ignored; `tsk project` warns if it is not.
3. Run the project's setup command in the worktree, if configured.
4. Create an OpenCode session with the worktree as its directory and
   `<id>: <title>` as its title.
5. Write `tsk.session`, `tsk.worktree` and `tsk.branch` to the task.
6. Send the task prompt to the session through the API.
7. In the herdr workspace for the repo (found by directory, or created), open a
   tab named `<id> <title>` running `opencode --session <id>` in the worktree.

On failure, completed steps are undone in reverse order: unclaim, remove a
worktree created by this dispatch, delete the session. A failure in step 7 does
not undo anything, because the agent is already working; the view reports the
missing tab and Enter can open it later.

The default prompt contains the task ID, title, description and acceptance
criteria, and these rules:

- Work only in this worktree.
- Commit on this branch with a `Task: <id>` trailer. Never push or merge.
- Ask for input with the question tool.
- When done, run `bd update <id> --status review --notes "<summary>"`.

The repository's `AGENTS.md` still applies.

## Configuration

`~/.config/tsk/config.toml`, all keys optional:

```toml
roots = ["~/gits"]
hidden = []
reread_interval = "10s"
```

`.tsk.toml` in a repository, optional and committed:

```toml
setup = "make worktree-setup"
prompt = """…"""
```

herdr keybind for the board:

```toml
[[keys.command]]
type = "popup"
command = "tsk board"
```

`tsk project` checks for the `review` status and offers to run
`bd config set status.custom "review:wip"` when it is missing.

## Error handling

- A missing `bd`, Dolt server, OpenCode service or herdr is named with how to
  start it. The board works without herdr; jumping and dispatch are disabled.
- Event-stream disconnects trigger reconnect with backoff and a full reload.
- No secret is ever logged or written to Beads.

## Testing

- `status`: table tests for all seven rules and the unknown state.
- `dispatch`: fake adapters, with a failure injected at each step to check the
  undo sequence.
- `opencode`: an `httptest` server replaying recorded responses and
  event-stream data, including dropped connections.
- `ui`: messages fed straight into the Bubble Tea models, checking `View()`
  output and the commands returned. (`teatest` for Bubble Tea v2 has no tagged
  release.)
- End to end, behind a build tag: a throwaway Beads project, real OpenCode
  sessions and real herdr tabs. It never touches real projects.

## Assumptions to verify first

Already confirmed read-only on 2026-10-06, from the live OpenAPI document
(`GET /openapi.json`), a sample of the event stream, OpenCode's source and
`bd`/`herdr` help output:

- Clients authenticate with HTTP basic auth, user `opencode`, password from
  `~/.config/opencode/service.json`. `opencode service status` prints the URL.
- `POST /api/session` takes `title`, `location.directory` and `metadata`.
- `GET /api/session/{id}/form` and `/permission` list pending requests per
  session; `GET /api/session?parentID=` lists child sessions.
- Events are `data:` lines carrying `{id, type, location, data, durable}`;
  most payloads carry `data.sessionID`, and `form.created` carries
  `data.form.sessionID`.
- `bd update --set-metadata k=v` merges keys; `bd config get status.custom
  --json` returns `{"value": …}`; `bd ready` and `bd list` accept `-n 0`.
- `herdr tab create` returns the new tab's `root_pane`; `herdr agent list`
  reports each pane's `agent_session.value`.

Still to exercise against the running services in the first implementation
task, with results recorded in `docs/contract-check.md`. If any fails, this
spec is revised before building on it.

- `opencode --session <id>` attaches to a session created through the API.
- `POST /api/session/{id}/prompt` starts work with no window attached.
- A question raised by the agent produces `form.created` and shows in the
  session's form list; going idle produces `session.idle` or
  `session.execution.succeeded`.
- `bd list --json` includes a `metadata` object; `bd update --assignee ""`
  clears the assignee; how `--claim` fails on a task claimed by another actor.
- `herdr pane run` starts a command in a new tab's pane, the pane then reports
  the session in `agent list`, and `herdr agent focus <pane>` switches to it.
