# tsk

Dispatch [Beads](https://github.com/gastownhall/beads) tasks to OpenCode agents
in [herdr](https://herdr.dev) tabs, and see the live state of every dispatched
task in bt.

## Requirements

- `bd`, with projects on the shared Dolt server
- the OpenCode v2 background service (`opencode service start`)
- `herdr`, for opening and jumping to tabs
- `bt` with plugin support

Dispatched agents use OpenCode's default model, so set a reliable default.

## Install

From a checkout:

```sh
make install   # go install ./cmd/tsk, into $GOBIN (default ~/go/bin)
```

Or without cloning:

```sh
go install github.com/AntoineGS/taskestrator/cmd/tsk@latest
```

## Use

tsk is a bt plugin: it has no screen
of its own. bt starts `tsk plugin` and shows each task's agent state next to
its other fields. Add it to `~/.config/bt/config.yaml`:

```yaml
plugins:
  - name: tsk
    command: ["tsk", "plugin"]
    options:
      roots: ["~/gits"]   # where to look for .beads/metadata.json
      model: "anthropic/claude-opus-5-5"   # optional: provider/id[#variant]
```

`model` sets the model of the OpenCode sessions tsk creates. Without it, they
use OpenCode's default model. An invalid value shows a warning and falls back
to the default.

Run `bt -g` for the global view across projects. On a task:

- `D` dispatches it: tsk claims the task, creates `.worktrees/<id>` on branch
  `task/<id>`, starts an OpenCode session with the task as its prompt, and
  opens a herdr tab attached to that session. It also re-dispatches a task whose
  session is lost. Tasks bt reports as blocked are not offered for dispatch.
- `J` jumps to the task's tab, reopening it if it was closed.
- `P` opens the plugin action menu.

The agent state is a BQL field, for example `tsk.state = waiting`. States are
`waiting` (on a question, permission or idle), `running`, `review`, `lost`,
`unknown` (OpenCode unreachable) and `queued`.

The agent finishes by running `bd update <id> --status review`. Review the
branch, merge it yourself, then run `bd close <id>`.

### Bind bt to a key in herdr

```toml
[[keys.command]]
type = "popup"
command = "bt -g --popup"
```

### Errors

When an action cannot run, bt shows tsk's message. You may see these messages
(the type is for reference; tsk returns it as `data.type`, and logs dispatch
failures to stderr):

| Message | Type | Meaning |
|---|---|---|
| no checkout found for `<db>`; add its parent to roots | `no_repo` | no checkout was found for the task's database |
| cannot tell which checkout `<id>` belongs to; add its parent to roots | `no_repo` | the task's checkout is unknown, so its tab cannot be found |
| herdr is not running | `no_herdr` | start herdr |
| `<id>` is blocked | `not_ready` | bt reports open blockers (status `blocked`, or an open `blocks` dependency) |
| this project has no review status; run: … | `no_review_status` | the project lacks the `review` status and you declined to add it |
| .worktrees/ is not git-ignored in this repo; … | `worktrees_not_ignored` | add `.worktrees/` to `.gitignore` |
| `<id>` is already being dispatched / was just dispatched; … | `busy` | wait for bt to refresh |
| `<id>` is `<state>` | `not_dispatchable` | the task is not queued or lost |
| (the `bd` or `git` error) | `check_failed` | a pre-dispatch check failed |
| (the failing step) | `dispatch_failed` | a dispatch step failed; earlier steps are undone |
| `<id>` has not been dispatched | `not_dispatched` | the task has no agent session to jump to |
| `<id>`'s agent session is gone; press D … | `session_lost` | dispatch the task again |
| (the herdr error) | `jump_failed` | the tab could not be focused or reopened |

tsk scans `roots` for checkouts once per plugin process, so a repository
cloned after bt started is found after bt restarts.

### Per-project setup

Each project needs the `review` status once. When it is missing, dispatching
asks whether to add `review:wip` to the project's bd config; you can also run
`bd config set status.custom "review:wip"` yourself. `.worktrees/` must be
git-ignored.

An optional `.tsk.toml` in the repository:

```toml
setup = "make worktree-setup"   # run in each new worktree before the agent starts
prompt = """..."""              # Go template; fields: ID Title Description Acceptance Worktree Branch
```
