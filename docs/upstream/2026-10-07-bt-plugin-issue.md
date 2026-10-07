# Plugin support: out-of-process plugins over JSON-RPC, built on in-process slots

## Purpose

bt is a capable Beads TUI with a global, cross-project mode. I want a plugin
that tracks coding agents working on beads: a live badge on dispatched beads
(waiting on you, running, review, lost), the session and pending question in
the detail pane, one key to dispatch a ready bead and one to jump to the
agent's terminal tab. Rather than rebuild bt's views elsewhere, I propose a
generic plugin system in bt. Nothing in bt would name any particular plugin.

Success means:

- Beads carry a live plugin badge that changes within a second of the plugin's
  event, with no Dolt write.
- One key on a bead runs a plugin action.
- A plugin field works in BQL (`plugin.field = value`), and the board can group
  beads by it across every project.
- The bead detail pane shows plugin sections.

## Decisions

| Topic | Decision |
|---|---|
| Plugin model | Out-of-process executables speaking JSON-RPC 2.0 over stdio. Language-agnostic; a plugin crash cannot take bt down; plugin code is never linked into bt. |
| Contributions | Data only. Plugins contribute badges, detail sections, BQL fields, swimlanes and actions; bt renders everything with its own theme. |
| Extension seams | In-process Go registries ("slots") that bt's built-in features also use. The plugin host is one provider among others. |
| Plugin location | User config only (`~/.config/bt/config.yaml`). No project-level plugins. |
| Lifetime | Plugins run only while the interactive TUI runs; never in `bt robot`. |
| Keys | Actions have stable IDs and default keys; users rebind or disable them per ID. bt's own keys always win. Every action is also in a plugin action menu. |
| Action safety | bt never replays an action after a crash, timeout or disconnect. |
| Popup mode | `bt --popup` lets an action result close bt (`dismiss: true`). Without the flag, `dismiss` is ignored. |

## Configuration and lifecycle

`~/.config/bt/config.yaml` gains:

```yaml
plugins:
  - name: example             # unique; prefixes BQL fields and action IDs
    command: ["example", "plugin"] # argv, never run through a shell
    enabled: true             # default true
    env: {}                   # added to bt's environment
    options: {}               # opaque; sent in initialize
    keys:                     # overrides by action ID; "none" disables
      dispatch: D
```

`name` must match `^[a-z][a-z0-9_]*$`. Duplicate names disable the later entry.

1. After the first snapshot is ready, bt starts every enabled plugin with its
   working directory set to bt's.
2. bt sends `initialize`. The plugin must answer within 5s with its manifest.
   The plugin is ready only once the manifest is validated; before that, bt sends
   it nothing else and accepts nothing else from it.
3. bt sends `beads.sync`, then keeps sending it as the data changes.
4. On quit, bt sends `shutdown` (notification), closes the plugin's stdin,
   waits 2s, then kills the process.
5. If the process exits unexpectedly, bt drops every contribution of that plugin,
   fails its in-flight actions as "result unknown", and restarts it after 1s,
   5s and 25s. After the third failure it stays failed until bt restarts.

All plugin I/O runs in goroutines that hand results to the update loop as
`tea.Msg` values, in the same way as the background worker. The UI never waits
on a plugin.

## Transport

- JSON-RPC 2.0, one JSON object per line (UTF-8, `\n`), on the plugin's
  stdin (bt to plugin) and stdout (plugin to bt). Lines are limited to 4 MiB.
- The plugin's stderr is copied to bt's debug log, prefixed with the plugin name.
- Requests carry an `id`; notifications do not. Either side may send requests.
- System errors use the standard JSON-RPC codes (-32700 to -32603). A plugin
  failure is code `-32000` with `data.type` set to a plugin-defined string, for
  example `{"code": -32000, "message": "no checkout", "data": {"type": "no_repo"}}`.
- Timeouts: `initialize` 5s; `action.invoke` 60s; `ui.*` requests from the
  plugin have no timeout of their own (they wait for the user) but are cancelled
  when the action they belong to ends.

## Protocol (version 1)

### Bead references

```json
{ "db": "proj", "id": "proj-a1b", "repo": "/home/u/gits/proj" }
```

`db` is the Beads database name: `Issue.SourceRepo` in global mode, the
project's Dolt database (the one in the data source's DSN) in project mode,
because the local reader copies the row's `source_repo` column instead. `repo` is the
checkout path when `bdroute` resolves one, otherwise `null`. Plugins key all
state by `(db, id)`.

### `initialize` (bt to plugin, request)

Params:

```json
{
  "protocolVersion": 1,
  "bt": { "version": "0.4.0" },
  "scope": { "mode": "global", "databases": ["proj", "other"] },
  "popup": false,
  "options": { "roots": ["~/gits"] }
}
```

`scope.mode` is `project`, `global` or `workspace`. Result: the manifest.

```json
{
  "protocolVersion": 1,
  "name": "example",
  "version": "0.3.0",
  "subscribe": {
    "metadata_prefixes": ["example."],
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

### `beads.sync` (bt to plugin, notification)

```json
{
  "revision": 12,
  "beads": [{
    "db": "proj", "id": "proj-a1b", "repo": "/home/u/gits/proj",
    "status": "in_progress", "title": "…", "type": "task", "priority": 2,
    "assignee": "antoine", "metadata": { "example.session": "ses_…" }
  }]
}
```

Always the full set of beads matching `subscribe`. Sent once after the manifest
is accepted, then whenever a snapshot's data hash changes. `revision` increases
by one each time. Descriptions and comments are never sent.

### `state.set` and `state.clear` (plugin to bt, notifications)

```json
{
  "replace": false,
  "beads": [{
    "db": "proj", "id": "proj-a1b",
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

### `action.invoke` (bt to plugin, request)

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

### `ui.confirm` and `ui.select` (plugin to bt, requests)

Allowed only while an `action.invoke` from the same plugin is in flight;
otherwise they fail with `-32000`, `data.type` `no_action`.

- `ui.confirm`: `{ "title", "message", "confirm"?, "cancel"? }` returns `true`,
  `false`, or `null` if dismissed.
- `ui.select`: `{ "title", "options": [{ "value", "label", "description"? }] }`
  returns the chosen `value` or `null`.

bt draws both with `OverlayCenterDimBackdrop`.

### `ui.toast` (plugin to bt, notification)

`{ "message", "tone"? }`. Allowed at any time. bt shows at most one toast per
plugin per second and drops the rest.

### `shutdown` (bt to plugin, notification)

No params. The plugin should exit promptly.

## Slots (in-process seams)

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
    // Fields lists BQL field names this provider answers, e.g. "example.state".
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
  badge then also appears on board cards, tree rows and epics rows.

## Selected bead

A single `selectedIssue() *model.Issue` on the model returns the bead under the
cursor in the list, board, tree or epics view (nil on project header rows).
Plugin actions, the action menu and the pending spinner use it.

## Keys and the action menu

- Each available action binds its configured key (the `keys` override, else
  the manifest default) in the list, board, tree and epics views.
- A key already bound by bt in that view, or by an earlier plugin in config
  order, is not bound; the conflict is listed in `bt plugins`.
- The plugin action menu opens on one bt key (proposed `v`, the only lowercase
  letter free in both list and board). It lists the actions available on the
  selected bead, grouped by plugin, with their keys.
- Plugin actions appear in the footer and shortcuts sidebar through the active
  keymap's help, like built-in bindings.

## Board lanes

The board's `[4][]model.Issue` becomes a slice of lanes, and the snapshot's
`BoardState` cache is keyed by grouping mode. Each plugin field adds a swimlane
mode to the existing cycle, named after the field label. Its lanes are the
field's values without `lane: false`, in manifest order. Beads with no value, or
a non-lane value, are not shown in that mode.

## `bt plugins`

Prints each configured plugin: name, command, status (`disabled`, `starting`,
`active`, `failed`), plugin version, restarts, last error, and key conflicts.
Supports `--json`. bt also shows a one-time footer notice when a plugin fails.

## Delivery as PRs

1. **Slots.** `pkg/ui/slots`, the badge helper in all four renderers, the
   detail section registry, dynamic BQL fields; move the overdue/stale badge
   and capabilities section. No plugin code. This is ready and tested.
2. **Board lanes.** Variable lane count and mode-keyed `BoardState` cache.
3. **Plugin host.** `pkg/plugin`, config, `initialize`, `beads.sync`,
   `state.set`/`state.clear`, badges, sections and BQL fields from plugins,
   `bt plugins`, a test plugin, `docs/plugins.md`.
4. **Plugin actions.** `selectedIssue()`, `action.invoke`, key binding and
   conflicts, the action menu, `ui.confirm`/`ui.select`/`ui.toast`, `--popup`.
5. **Plugin swimlanes.** One mode per plugin field, on top of PR 2.

Each PR follows bt's conventions: `type(scope): description` commits, README key
tables kept in sync (guarded by `readme_keys_test.go`), `go build`, `go vet` and
`go test ./...` clean.

## Questions

1. Is this approach acceptable (out-of-process JSON-RPC plugins on in-process slots, delivered as the PRs above)?
2. Which key should open the plugin action menu? I propose `v`.
