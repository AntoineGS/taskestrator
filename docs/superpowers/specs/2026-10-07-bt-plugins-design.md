# bt plugins, and tsk as the first plugin

Date: 2026-10-07
Status: implemented in the fork (first local version); follow-ups in beadstui
`bt-otb` and tsk `tsk-55e`, `tsk-kmz`

## Purpose

beadstui (`bt`) is a capable Beads TUI with a global, cross-project mode. tsk
needs a task-centric view with live agent state, one-key dispatch and jumping
to the agent's terminal tab. Rebuilding bt's views inside tsk is not worth it.
Instead, bt gains a generic plugin system in the fork `AntoineGS/beadstui`,
and tsk becomes a headless plugin that supplies the agent features. tsk's own
TUI is retired.

Success means:

- In `bt` (project or global mode), dispatched beads carry a live badge (waiting
  on you, running, review, lost) that changes within a second of the OpenCode
  event, with no Dolt write.
- One key on a ready bead dispatches it exactly as `tsk project`'s `d` did; one
  key on a dispatched bead jumps to its herdr tab.
- `tsk.state = waiting` works in BQL, and a board swimlane mode groups beads by
  agent state across every project.
- The bead detail pane shows the session, worktree, branch and pending
  question.
- The plugin system is generic: nothing in bt names tsk, OpenCode or herdr.

## Decisions

| Topic | Decision |
|---|---|
| Delivery | Branches merged into the fork `AntoineGS/beadstui`. Not upstreamed (decided 2026-10-08; the earlier upstream plan and drafts in `docs/upstream/` are abandoned). |
| Plugin model | Out-of-process executables speaking JSON-RPC 2.0 over stdio. Language-agnostic; a plugin crash cannot take bt down; plugin code is never linked into bt. |
| Contributions | Data only. Plugins contribute badges, detail sections, BQL fields, swimlanes and actions; bt renders everything with its own theme. |
| Extension seams | In-process Go registries ("slots") that bt's built-in features also use. The plugin host is one provider among others. |
| Plugin location | User config only (`~/.config/bt/config.yaml`). No project-level plugins. |
| Lifetime | Plugins run only while the interactive TUI runs; never in `bt robot`. |
| Keys | Actions have stable IDs and default keys; users rebind or disable them per ID. bt's own keys always win. Every action is also in a plugin action menu. |
| Action safety | bt never replays an action after a crash, timeout or disconnect. |
| tsk | Stays in Go. Becomes `tsk plugin`; `tsk board`, `tsk project` and the `ui` package are removed. |
| Popup mode | `bt --popup` lets an action result close bt (`dismiss: true`). Without the flag, `dismiss` is ignored. |

### Prior art: OpenCode v2 plugins

Read from `../opencode` (`packages/plugin`, `packages/tui/src/plugin`,
`packages/plugin-browser`, `specs/v2/catalog-config-plugin-lifecycle.md`).
Ideas adopted:

- A small tree of named, coarse, documented slots that survive host refactors;
  claims on a slot that no longer exists degrade instead of failing.
- Commands with stable IDs, user-rebindable keys (`none` disables), and a
  discovery surface.
- UI primitives (confirm, select, toast) owned by the host.
- Built-in features use the same seams as plugins.
- Every contribution is owned by its plugin and disappears when the plugin stops,
  with no manual undo.
- A plugin manager showing active/failed status and the last error.
- Versioned attach, a readiness barrier, reserved system error names, and no
  automatic replay of calls whose response was lost.

Not adopted: in-process loading, hot reload, plugin-scoped storage, plugin
pages/routes, `replace` placements, package installation.

## Non-goals

- Project-level (repository-committed) plugins and their trust model.
- Plugins in `bt robot` output.
- Plugin-rendered UI (raw ANSI, custom views or pages).
- Free-text input dialogs (`ui.prompt`).
- Hot reload of plugins.
- Answering agent questions or permissions from bt.
- Any automatic push, merge or rebase (unchanged from tsk).

## Part 1: bt

### Configuration

`~/.config/bt/config.yaml` (already read by bt) gains:

```yaml
plugins:
  - name: tsk                 # unique; prefixes BQL fields and action IDs
    command: ["tsk", "plugin"] # argv, never run through a shell
    enabled: true             # default true
    env: {}                   # added to bt's environment
    options:                  # opaque; sent in initialize
      roots: ["~/gits"]
    keys:                     # overrides by action ID; "none" disables
      dispatch: D
      jump: J
```

`name` must match `^[a-z][a-z0-9_]*$`. Duplicate names disable the later entry.

### Lifecycle

1. After the first snapshot is ready, bt starts every enabled plugin with its
   working directory set to bt's.
2. bt sends `initialize`. The plugin must answer within 5s with its manifest.
   The plugin is ready only once the manifest is validated; before that, bt sends
   it nothing else and accepts nothing else from it.
3. bt sends `beads.sync`, then keeps sending it as the data changes.
4. On quit, from `Model.Stop()`: bt sends `shutdown` (notification), closes the
   plugin's stdin, waits 2s, then kills the process.
5. If the process exits unexpectedly, bt drops every contribution of that plugin,
   fails its in-flight actions as "result unknown", and restarts it after 1s,
   5s and 25s. After the third failure it stays failed until bt restarts.

All plugin I/O runs in goroutines that hand results to the update loop as
`tea.Msg` values, in the same way as the background worker. The UI never waits
on a plugin.

### Transport

- JSON-RPC 2.0, one JSON object per line (UTF-8, `\n`), on the plugin's
  stdin (bt to plugin) and stdout (plugin to bt). Lines are limited to 4 MiB.
- The plugin's stderr is copied to bt's debug log, prefixed with the plugin name.
- Requests carry an `id`; notifications do not. Either side may send requests.
- System errors use the standard JSON-RPC codes (-32700 to -32603). A plugin
  failure is code `-32000` with `data.type` set to a plugin-defined string, for
  example `{"code": -32000, "message": "no checkout for tsk", "data": {"type": "no_repo"}}`.
- Timeouts: `initialize` 5s; `action.invoke` 60s; `ui.*` requests from the
  plugin have no timeout of their own (they wait for the user) but are cancelled
  when the action they belong to ends.

### Protocol (version 1)

#### Bead references

```json
{ "db": "tsk", "id": "tsk-a1b", "repo": "/home/u/gits/taskestrator" }
```

`db` is the Beads database name: `Issue.SourceRepo` in global mode, the
project's Dolt database (the one in the data source's DSN) in project mode,
because the local reader copies the row's `source_repo` column instead. `repo` is the
checkout path when `bdroute` resolves one, otherwise `null`. Plugins key all
state by `(db, id)`.

#### `initialize` (bt to plugin, request)

Params:

```json
{
  "protocolVersion": 1,
  "bt": { "version": "0.4.0" },
  "scope": { "mode": "global", "databases": ["tsk", "se"] },
  "popup": false,
  "options": { "roots": ["~/gits"] }
}
```

`scope.mode` is `project`, `global` or `workspace`. Result: the manifest.

```json
{
  "protocolVersion": 1,
  "name": "tsk",
  "version": "0.3.0",
  "subscribe": {
    "metadata_prefixes": ["tsk."],
    "statuses": ["open", "in_progress", "review", "blocked"]
  },
  "fields": [{
    "id": "state",
    "label": "Agent",
    "values": [
      { "id": "waiting", "label": "Waiting on you", "badge": "WAIT", "tone": "warn" },
      { "id": "running", "label": "In progress",    "badge": "RUN",  "tone": "accent" },
      { "id": "review",  "label": "Review",         "badge": "REV",  "tone": "ok" },
      { "id": "lost",    "label": "Lost",           "badge": "LOST", "tone": "error" },
      { "id": "unknown", "label": "Unknown",        "badge": "?",    "tone": "muted", "lane": false },
      { "id": "queued",  "label": "Queued",                                           "lane": false }
    ]
  }],
  "sections": [{ "id": "agent", "title": "Agent" }],
  "actions": [
    { "id": "dispatch", "label": "Dispatch to agent", "key": "D" },
    { "id": "jump",     "label": "Jump to agent tab", "key": "J" }
  ]
}
```

Validation, failing which the plugin is disabled:

- `protocolVersion` equals bt's.
- `name` equals the config entry's `name`.
- IDs of fields, values, sections and actions match `^[a-z][a-z0-9_]*$` and are
  unique within their list.
- `tone` is one of `accent`, `ok`, `warn`, `error`, `muted`; it is mapped to
  theme colors by bt.
- `badge` is at most 6 cells wide.

`subscribe.statuses` omitted means every status; `metadata_prefixes` omitted
means no metadata is sent.

#### `beads.sync` (bt to plugin, notification)

```json
{
  "revision": 12,
  "beads": [{
    "db": "tsk", "id": "tsk-a1b", "repo": "/home/u/gits/taskestrator",
    "status": "in_progress", "title": "…", "type": "task", "priority": 2,
    "assignee": "antoine", "metadata": { "tsk.session": "ses_…" }
  }]
}
```

Always the full set of beads matching `subscribe`. Sent once after the manifest
is accepted, then whenever a snapshot's data hash changes. `revision` increases
by one each time. Descriptions and comments are never sent.

#### `state.set` and `state.clear` (plugin to bt, notifications)

```json
{
  "replace": false,
  "beads": [{
    "db": "tsk", "id": "tsk-a1b",
    "fields": { "state": { "value": "waiting", "since": "2026-10-07T14:03:00Z" } },
    "sections": { "agent": "**Session** `ses_…`\n\nWaiting on a question: …" },
    "actions": ["jump"]
  }]
}
```

- Each entry replaces that bead's previous entry from this plugin. With
  `replace: true`, everything the plugin pushed before is dropped first.
- `fields` values must be declared in the manifest; unknown fields or values
  drop that field with a debug log line. `since` is optional; when present the
  badge shows the age ("WAIT 4m").
- `sections` values are markdown, at most 16 KiB each.
- `actions` lists the actions available on this bead. A bead without an entry
  from the plugin has none of its actions.
- Entries for beads bt does not currently hold are kept, and show up if the bead
  appears later.

`state.clear` takes `{ "beads": [{ "db": "…", "id": "…" }] }` and removes those
entries.

bt redraws at most every 100ms in response to state pushes.

#### `action.invoke` (bt to plugin, request)

Params: `{ "action": "dispatch", "bead": { "db", "id", "repo" }, "view": "list" }`,
where `view` is `list`, `board`, `tree` or `epics`.

Result: `{ "toast": { "message": "…", "tone": "ok" }, "dismiss": false }`, both
fields optional. An error result shows its `message` as an error toast.

While the call runs, the bead shows bt's pending spinner. The spinner clears on
the result, on the next `state.set` entry for that bead, or on the 60s timeout,
whichever comes first. A timeout or plugin crash shows "result unknown" and is
never retried. One action per bead at a time; a second request on the same bead
is refused with a notice, matching bt's existing claim behaviour.

`dismiss: true` quits bt only when bt was started with `--popup`.

#### `ui.confirm` and `ui.select` (plugin to bt, requests)

Allowed only while an `action.invoke` from the same plugin is in flight;
otherwise they fail with `-32000`, `data.type` `no_action`.

- `ui.confirm`: `{ "title", "message", "confirm"?, "cancel"? }` returns `true`,
  `false`, or `null` if dismissed.
- `ui.select`: `{ "title", "options": [{ "value", "label", "description"? }] }`
  returns the chosen `value` or `null`.

bt draws both with `OverlayCenterDimBackdrop`.

#### `ui.toast` (plugin to bt, notification)

`{ "message", "tone"? }`. Allowed at any time. bt shows at most one toast per
plugin per second and drops the rest.

#### `shutdown` (bt to plugin, notification)

No params. The plugin should exit promptly.

### Slots (in-process seams)

New package `pkg/ui/slots`. Built-in features and the plugin host both
register providers.

```go
type Tone int // Accent, OK, Warn, Error, Muted

type Badge struct {
    Text  string
    Tone  Tone
    Since time.Time       // zero: no age shown
    Style *lipgloss.Style // built-in providers only; overrides Tone
}

type BadgeProvider interface {
    Badges(issue *model.Issue) []Badge
}

type Section struct {
    Title    string
    Markdown string
}

// Context carries view state a provider may depend on.
type Context struct {
    WorkspaceMode bool
}

type SectionProvider interface {
    Sections(issue *model.Issue, ctx Context) []Section
}

type FieldProvider interface {
    // Fields lists BQL field names this provider answers, e.g. "tsk.state".
    Fields() []string
    Value(issue *model.Issue, field string) (string, bool)
}
```

- Badges render through one helper used by all four row renderers (list
  delegate, board card, tree node, epics rows). They sit just before the ID,
  share a capped width budget, and are dropped before the title is truncated
  on narrow terminals.
- Sections render directly after the property block (where Capabilities sits
  today), in provider registration order, so they are visible without
  scrolling past comments and history.
- BQL validation in the UI uses `ValidateWithFields` with the built-in fields
  plus every registered provider field; `MemoryExecutor` consults a
  `FieldResolver` in `ExecuteOpts` for fields it does not know. A bead without a
  value does not match any comparison on that field.
- The overdue/stale badge and the capabilities detail section move onto these
  registries, so that built-in code exercises the seams. The overdue/stale
  badge then also appears on board cards, tree rows and epics rows. (The gate
  badge stays where it is: it depends on `IssueItem.GateAwaitType`, which is
  computed from the bead's blockers, not from the bead itself.)

### Selected bead

A single `selectedIssue() *model.Issue` on the model returns the bead under the
cursor in the list, board, tree or epics view (nil on project header rows).
Plugin actions, the action menu and the pending spinner use it.

### Keys and the action menu

- Each available action binds its configured key (the `keys` override, else
  the manifest default) in the list, board, tree and epics views.
- A key already bound by bt in that view, or by an earlier plugin in config
  order, is not bound; the conflict is listed in `bt plugins`.
- The plugin action menu opens on one bt key (proposed `v`, the only lowercase
  letter free in both list and board; the maintainer decides). It lists the
  actions available on the selected bead, grouped by plugin, with their keys.
- Plugin actions appear in the footer and shortcuts sidebar through the active
  keymap's help, like built-in bindings.

### Board lanes

The board's `[4][]model.Issue` becomes a slice of lanes, and the snapshot's
`BoardState` cache is keyed by grouping mode. Each plugin field adds a swimlane
mode to the existing cycle, named after the field label. Its lanes are the
field's values without `lane: false`, in manifest order. Beads with no value, or
a non-lane value, are not shown in that mode.

### `bt plugins`

Prints each configured plugin: name, command, status (`disabled`, `starting`,
`active`, `failed`), plugin version, restarts, last error, and key conflicts.
Supports `--json`. bt also shows a one-time footer notice when a plugin fails.

### `--popup`

A TUI flag. It only changes how `dismiss` is handled, and is passed to plugins
in `initialize`.

### Delivery

The plugin system lives in the fork `AntoineGS/beadstui` only; it is not
upstreamed (decided 2026-10-08). The work is split into these steps:

1. **Slots.** `pkg/ui/slots`, the badge helper in all four
   renderers, the detail section registry, dynamic BQL fields; move the
   overdue/stale badge and capabilities section. No plugin code. Merged
   (`feat/plugin-slots`).
2. **Board lanes.** Variable lane count and mode-keyed `BoardState` cache.
   Open: `bt-otb.5`.
3. **Plugin host.** `pkg/plugin`, config, `initialize`, `beads.sync`,
   `state.set`/`state.clear`, badges, sections and BQL fields from plugins,
   `bt plugins`, the `testdata` test plugin, `docs/plugins.md`. Merged
   (`feat/plugin-host`).
4. **Plugin actions.** `selectedIssue()`, `action.invoke`, key binding and conflicts, the action
   menu, `ui.confirm`/`ui.select`/`ui.toast`, `--popup`. Merged
   (`feat/plugin-host`).
5. **Plugin swimlanes.** One mode per plugin field, on top of step 2.
   Open: `bt-otb.6`.

The remaining bt work is tracked under the `bt-otb` epic in beadstui. Each
branch follows bt's conventions: `type(scope): description` commits, README key
tables kept in sync (guarded by `readme_keys_test.go`), `go build`, `go vet` and
`go test ./...` clean.

## Part 2: tsk

### Command

`tsk plugin` speaks protocol version 1 on stdio. `tsk board` and `tsk project`
are removed. The herdr popup keybind becomes:

```toml
[[keys.command]]
type = "popup"
command = "bt -g --popup"
```

### Packages

| Package | Change |
|---|---|
| `ui` | Removed. |
| `opencode`, `herdr`, `dispatch`, `status`, `tracker` | Unchanged. |
| `discover` | Reduced to resolving a database to a checkout under `roots` when bt sends `repo: null`. |
| `btplugin` (new) | Codec, manifest, and the loop that combines `beads.sync` with OpenCode snapshots and events into `state.set`. |
| `cmd/tsk` | Only the `plugin` subcommand. |

### State mapping

The existing `status` rules, applied to each synced bead:

| `status` result | Push |
|---|---|
| done (closed) | `state.clear` |
| untracked (no `tsk.session`, not open) | `state.clear` |
| review | `review`, no actions |
| queued | `queued`, `[dispatch]` |
| lost | `lost`, `[dispatch]` |
| waiting (question, permission or idle) | `waiting`, `[jump]` |
| in progress | `running`, `[jump]` |
| unknown (OpenCode unreachable) | `unknown`, `[jump]`, plus one `ui.toast` naming how to start the service |

`since` is the time the bead entered its current state, as tracked by tsk since
it started (or the session's last activity on first sight). The `agent`
section shows the session ID, worktree, branch, the waiting reason and, for a
question, its text.

`dispatch` is omitted from `actions` when the bead's checkout cannot be resolved
or herdr is not running; `jump` is omitted when herdr is not running.

### Live updates

- On `beads.sync`, tsk recomputes every bead and pushes the differences. A bead
  that tsk pushed before and that is missing from the new sync (closed, or no
  longer matching `subscribe`) gets `state.clear`.
- OpenCode events update session snapshots as today and push the affected
  beads only.
- After an event-stream reconnect, tsk reloads all sessions and pushes with
  `replace: true`.
- The 10s Beads re-read is removed; `beads.sync` covers the agent's own
  `bd update --status review`.

### Actions

- `dispatch`: the existing dispatch steps and undo, unchanged. Returns a toast
  with the session ID, or an error naming the failed step.
- `jump`: focuses the pane whose `agent_session` matches, or reopens the tab
  with `opencode --session <id>` and the existing 15s focus retry. Returns
  `dismiss: true`.

### Configuration

- `~/.config/tsk/config.toml` is removed. `roots` moves to the plugin's
  `options`; `hidden` (replaced by bt's repo filter) and `reread_interval`
  (replaced by `beads.sync`) are dropped.
- `.tsk.toml` in a repository (`setup`, `prompt`) is unchanged.
- The first time tsk sees a database in `beads.sync`, it runs
  `bd -C <repo> config get status.custom --json`. If `review` is missing, it
  sends one `ui.toast` saying the first dispatch there will ask to set it up.
- Dispatch runs a project setup check: `.worktrees/` git-ignored and the
  `review` status present. When anything is missing, one `ui.confirm` lists
  the missing steps; on confirm tsk appends `.worktrees/` to `.gitignore`
  (uncommitted), adds `review:wip` to `status.custom` (keeping other custom
  statuses), re-checks, and continues the dispatch. Declining refuses with
  `needs_setup` and the manual steps. tsk never changes a project's files or
  Beads configuration without that confirmation (changed 2026-10-09,
  `tsk-pg5`, `tsk-bri`).

## Error handling

bt:

- Plugin fails to start, answers `initialize` late, or sends an invalid
  manifest: marked failed, one footer notice, details in `bt plugins`.
- Malformed line or unknown method from a plugin: logged and dropped. After 10
  such lines the plugin is killed and treated as crashed.
- Requests to the plugin answered with an unknown `id`: logged and dropped.
- A plugin never blocks rendering or input.

tsk:

- Unchanged dispatch, OpenCode and herdr error handling.
- No secret is logged, sent in a toast, or written to a section.
- If stdin closes, `tsk plugin` exits.

## Testing

bt:

- `slots`: table tests for badge layout at several widths in all four renderers,
  section ordering, and BQL with provider fields (matches, missing values,
  validation errors).
- Board lanes: existing board tests pass unchanged; new tests for a mode with 3
  and 5 lanes.
- Codec: property tests with `rapid` for encoding round trips and line framing.
- Host: a Go test plugin built from `testdata/` and scripted per test, covering
  handshake, version and name mismatch, invalid manifest, `state.set` and
  `replace`, redraw coalescing, action success, error and timeout, crash during
  an action (reported as unknown, not replayed), restart backoff, `ui.confirm`
  inside and outside an action, key conflicts, and `dismiss` with and without
  `--popup`.
- UI: messages fed into the model, checking `View()` output, as bt's existing
  tests do.

tsk:

- `status` and `dispatch` tests unchanged.
- Table test for the state mapping above.
- `btplugin`: a fake host drives `tsk plugin` through `initialize`, `beads.sync`,
  `action.invoke` and `shutdown`, with fake OpenCode and herdr adapters.
- `make e2e`: real `bd`, OpenCode service and herdr, with the fake host in place
  of bt.

## First local version (2026-10-07)

The user wanted the feature usable quickly. The first version was built on
local stacked branches and narrows a few points of this spec; the follow-up
beads listed with each point widen them.

- The plugin action menu opens with `P`. `v` is bound in the epics view, and
  `P` is free in the list, board, tree and epics views.
- Plugin actions are listed only in the action menu, not in the footer or
  shortcuts sidebar (`bt-otb.3`).
- The pending spinner shows on list rows only; in the board, tree and epics
  views a notice ("Dispatching tsk-a1b…") is shown instead (`bt-otb.4`). The
  45s timeout is also checked on the spinner tick, not only on data reloads.
- `bt plugins` prints the configured plugins, their resolved commands and
  whether they are enabled. Runtime failures appear as footer notices
  (`bt-otb.2`).
- Board swimlanes per plugin field (steps 2 and 5) are deferred; BQL filters
  (`tsk.state in (waiting, lost)`) cover grouping meanwhile (`bt-otb.5`,
  `bt-otb.6`).
- Resolved 2026-10-08 (`bt-otb.1`, `tsk-55e`): `beads.sync` now carries
  `updated_at` and `blocked`. A bead's first state starts at `updated_at`
  (the time tsk first saw it when `updated_at` is unset or in the future), and
  tsk no longer calls `bd ready`: blocked beads get no dispatch action and
  `dispatch` refuses them.
- herdr is considered running when `herdr status server` prints
  `status: running`.

## Assumptions

Checked on 2026-10-07 against beadstui `071a0794` and bd 1.3.1:

- Project mode: `Issue.SourceRepo` is not the database name (the local reader
  copies the `source_repo` column), but the database name is in the data
  source's DSN, so the host can fill `db` from it.
- `bd update --set-metadata` bumps `updated_at` (observed 18:07:01 to
  18:07:03 on a throwaway project). bt's data hash includes `UpdatedAt` but not
  `Metadata`, and its poller reloads on a newer modification time, so metadata
  writes do reach `beads.sync`.
- `review` is a valid status in `pkg/model` and in BQL. The status board has no
  Review lane; review beads appear in Open. Unchanged by this spec.
- `D` and `J` are unbound in the list, board, tree and epics keymaps. `J` is
  bound in History and `D` in the AGENTS.md prompt modal, neither of which
  dispatches plugin keys.

Still to check by hand: herdr popups pass the terminal size and keys through to
bt as they did for `tsk board` (`tsk-kmz`).
