# tsk

Dispatch [Beads](https://github.com/gastownhall/beads) tasks to OpenCode agents
in [herdr](https://herdr.dev) tabs, and see the live state of every dispatched
task in one board.

## Requirements

- `bd`, with projects on the shared Dolt server
- the OpenCode v2 background service (`opencode service start`)
- `herdr`, for opening and jumping to tabs

Dispatched agents use OpenCode's default model, so set a reliable default.

## Install

```sh
go install github.com/AntoineGS/taskestrator/cmd/tsk@latest
```

## Use

- `tsk project [dir]` lists the project's open tasks, ready ones first. `d`
  dispatches the task under the cursor: it claims the task, creates
  `.worktrees/<id>` on branch `task/<id>`, starts an OpenCode session with the
  task as its prompt, and opens a herdr tab attached to that session. `enter`
  jumps to a dispatched task. `d` also re-dispatches a task whose session is
  lost.
- `tsk board` lists dispatched tasks from every project, grouped as waiting on
  you, in progress, review and lost. `enter` jumps to the task's tab, reopening
  it if it was closed. `a` adds queued tasks and tasks closed in the last day;
  `/` filters.

The agent finishes by running `bd update <id> --status review`. Review the
branch, merge it yourself, then run `bd close <id>`.

### Bind the board to a key in herdr

```toml
[[keys.command]]
type = "popup"
command = "tsk board"
```

### Per-project setup

Each project needs the `review` status once; `tsk project` offers to add it
with `R`. `.worktrees/` must be git-ignored.

An optional `.tsk.toml` in the repository:

```toml
setup = "make worktree-setup"   # run in each new worktree before the agent starts
prompt = """..."""              # Go template; fields: ID Title Description Acceptance Worktree Branch
```

### Configuration

`~/.config/tsk/config.toml`, every key optional:

```toml
roots = ["~/gits"]        # where to look for .beads/metadata.json
hidden = []               # project names or paths to skip
reread_interval = "10s"
```
