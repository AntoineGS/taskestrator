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
```

Run `bt -g` for the global view across projects. On a task:

- `D` dispatches it: tsk claims the task, creates `.worktrees/<id>` on branch
  `task/<id>`, starts an OpenCode session with the task as its prompt, and
  opens a herdr tab attached to that session. It also re-dispatches a task whose
  session is lost.
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

An action that cannot run shows one of these in bt:

| Error | Meaning |
|---|---|
| `no_repo` | no checkout was found for the task's database; add its parent to `roots` |
| `no_herdr` | herdr is not running |
| `not_ready` | the task is blocked |
| `no_review_status` | the project lacks the `review` status |
| `worktrees_not_ignored` | `.worktrees/` is not git-ignored in the repository |
| `busy` | the task is already being dispatched, or was just dispatched |
| `not_dispatchable` | the task is not queued or lost |
| `check_failed` | a pre-dispatch check (`bd` or `git`) failed |
| `dispatch_failed` | a dispatch step failed; the message names it and earlier steps are undone |
| `not_dispatched` | the task has no agent session to jump to |
| `session_lost` | the agent session is gone; press `D` to dispatch again |
| `jump_failed` | the tab could not be focused or reopened |

### Per-project setup

Each project needs the `review` status once (tsk warns when it is missing; run
`bd config set status.custom "review:wip"`). `.worktrees/` must be git-ignored.

An optional `.tsk.toml` in the repository:

```toml
setup = "make worktree-setup"   # run in each new worktree before the agent starts
prompt = """..."""              # Go template; fields: ID Title Description Acceptance Worktree Branch
```
