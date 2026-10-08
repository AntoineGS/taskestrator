# tsk → `tsk plugin` integration notes

Read-only survey of the repo at commit 07717c7. Module `github.com/AntoineGS/taskestrator`, Go 1.27.1. Line numbers are for that commit.

## 1. Package map and exported API

| Package | Role |
|---|---|
| `cmd/tsk` | `main.go`: `board` / `project` subcommands, builds deps, runs Bubble Tea. |
| `internal/status` | Pure state derivation. |
| `internal/tracker` | Only caller of `bd` (`Tracker` interface, `BD` impl). |
| `internal/opencode` | HTTP client for the OpenCode v2 service, SSE events, snapshot, service discovery. |
| `internal/herdr` | Only caller of `herdr` CLI (`Herdr` interface, `CLI` impl). |
| `internal/dispatch` | 7-step dispatch with undo (`Run`), `Jump`, `OpenTab`, prompt template. |
| `internal/discover` | Scans `roots` for `.beads/metadata.json`; `Project`, `Scan`, `ForDir`. |
| `internal/config` | `~/.config/tsk/config.toml` (`Config`) and per-repo `.tsk.toml` (`ProjectFile`). |
| `internal/gitwt` | Only caller of `git`: worktree add/remove, default branch, `IsIgnored`. |
| `internal/execx` | `Runner` func type; `Command(bin)`; `Error`; `ExitCode`. |
| `internal/board` | UI-free `Store`: tasks per project + snapshots → `status.Display` + `since`. |
| `internal/ui` | Bubble Tea `Board` and `Project` models (to be removed). |
| `e2e` | Build-tag `e2e` test. |

### status (`internal/status/status.go`)
```go
type State int
const ( Queued State = iota; InProgress; Waiting; Review; Done; Lost; Unknown; Untracked )   // :8-17
func (s State) String() string        // "queued","in progress","waiting on you","review","done","lost","unknown","untracked"
type Snapshot struct { Exists, Running bool; Forms, Permissions int }                        // :29
type Display struct { State State; Detail string }                                          // :37
func Derive(taskStatus, sessionID string, snap *Snapshot) Display                            // :51
```
`Derive` order: closed→Done; review→Review; no session+open→Queued; no session otherwise→Untracked(Detail=status); snap==nil→Unknown; !Exists→Lost; Forms>0→Waiting"question"; Permissions>0→Waiting"permission"; Running→InProgress; else Waiting"idle".

### tracker (`internal/tracker/tracker.go`)
```go
const MetaSession="tsk.session"; MetaWorktree="tsk.worktree"; MetaBranch="tsk.branch"; StatusReview="review"   // :15
type Task struct {                                                                           // :23
  ID, Title string; Description string `json:"description"`; Acceptance string `json:"acceptance_criteria"`
  Status string; Priority int; Assignee string; Metadata map[string]any
  UpdatedAt time.Time; ClosedAt *time.Time }
func (t Task) Meta(key string) string                                                        // :37
type Tracker interface {                                                                     // :45
  Ready(ctx, repo string) ([]Task, error)
  List(ctx, repo string) ([]Task, error)            // `bd list --json --all -n 0`
  Claim(ctx, repo, id string) error                 // `bd update id --claim`
  Unclaim(ctx, repo, id string) error
  SetMetadata(ctx, repo, id string, kv map[string]string) error
  UnsetMetadata(ctx, repo, id string, keys ...string) error
  HasReviewStatus(ctx, repo string) (bool, error)   // `bd config get status.custom --json`
  AddReviewStatus(ctx, repo string) error }
type BD struct{ Run execx.Runner }; func New() BD
```
No `Show`/`Get` method exists. `bd` is always run with `dir=repo` (cwd), never `-C`.

### opencode
```go
// client.go
type Client struct { BaseURL, Password string; HTTP *http.Client; /* mu, parents unexported */ }   // :20
var ErrNotFound                                                                                     // :17
type Session struct { ID, ParentID, Title string; Location struct{ Directory string } }
type CreateSession struct { Title, Directory string; Metadata map[string]string }
func (c *Client) CreateSession(ctx, CreateSession) (Session, error)
func (c *Client) GetSession(ctx, id) (Session, error)
func (c *Client) DeleteSession(ctx, id) error
func (c *Client) Prompt(ctx, id, text string) error
func (c *Client) Children(ctx, id) ([]Session, error)        // GET /api/session?parentID=..&limit=100
func (c *Client) Active(ctx) (map[string]bool, error)        // GET /api/session/active
func (c *Client) FormCount(ctx, id) (int, error)             // GET /api/session/{id}/form  (len of data)
func (c *Client) PermissionCount(ctx, id) (int, error)       // .../permission
// snapshot.go
func (c *Client) Snapshot(ctx, id string, active map[string]bool) (status.Snapshot, error)  // :15 BFS over children, maxTree=64; 404 on root → Snapshot{} (Exists=false)
func (c *Client) Root(ctx, id string) (string, error)                                       // :58 walks ParentID, cached in c.parents
// events.go
type Event struct{ Type, SessionID string }                                                 // :14
func (c *Client) Subscribe(ctx) (<-chan Event, error)                                       // :21 GET /api/event SSE
func Relevant(t string) bool   // session.status|idle|deleted, session.execution.*, form.*, permission.*
func Settled(t string) bool    // session.idle, session.execution.{succeeded,failed,interrupted}
// service.go
func FromService(ctx) (*Client, error)                                                      // :17
```
- URL/password: `FromService` runs `opencode service status`, takes first line starting `http(s)://` (`parseServiceURL`), reads password from `os.UserConfigDir()/opencode/service.json` (`{"password":...}`; missing file = empty password). Basic auth user `opencode` (`client.go:59`). Errors say "start it with: opencode service start" — reuse in the `unknown` toast.
- Reconnect: `Subscribe` does **not** reconnect. Channel (buffer 64) closes when the stream ends or ctx is cancelled; the caller reconnects (see §2). No Last-Event-ID; state is rebuilt by a full reload.
- **Question text is not available anywhere today.** `FormCount` only counts entries. Per `docs/contract-check.md:16`, `GET /api/session/{id}/form` returns `[{id, sessionID, title:"Questions", metadata:{kind:"question",...}, fields:[{key,title,description,type,options:[{value,label,description}],custom}]}]`. A new method (e.g. `Forms(ctx,id) ([]Form, error)` reading `fields[0].title`) and a `Question string` field on a snapshot type are needed for the spec's "question text".
- Event payload: `parseEvent` (:82) takes session from `data.sessionID` or `data.form.sessionID`. `form.cancelled`/others use `data.sessionID`.

### herdr (`internal/herdr/herdr.go`)
```go
type Herdr interface {                                                                       // :16
  WorkspaceFor(ctx, repo string) (id string, found bool, err error)
  CreateWorkspace(ctx, cwd, label string) (string, error)
  CreateTab(ctx, workspaceID, cwd, label string) (paneID string, err error)
  Run(ctx, paneID string, command ...string) error
  PaneForSession(ctx, sessionID string) (paneID string, found bool, err error)             // `herdr agent list`, matches agent_session.value
  Focus(ctx, paneID string) error }                                                          // `herdr agent focus`
type CLI struct{ Exec execx.Runner }; func New() CLI
```
Availability check today is only `exec.LookPath("herdr")`. There is no "is herdr running" probe; the spec's "omit jump/dispatch when herdr is not running" needs a new check (e.g. a cheap `herdr pane list` call, whose failure yields `server_not_running`).

### dispatch (`internal/dispatch`)
```go
type OpenCode interface { CreateSession; DeleteSession; Prompt }                             // dispatch.go:21
type Deps struct { Tracker tracker.Tracker; OpenCode OpenCode; Herdr herdr.Herdr; Git gitwt.Git; Shell execx.Runner }  // :28
type Request struct { Project discover.Project; Task tracker.Task; File config.ProjectFile } // :37
type Result struct { SessionID, Worktree, Branch, PaneID string; TabError error }          // :45
func Run(ctx, d Deps, req Request) (Result, error)                                           // :58  (no constructor; Deps is a plain struct, there is no Dispatcher type)
// jump.go
type Target struct{ Repo, Project, Worktree, TaskID, Title, SessionID string }               // :16
func TargetFor(p discover.Project, t tracker.Task) Target                                    // :21
func OpenTab(ctx, h herdr.Herdr, t Target) (paneID string, err error)                        // :39
func Jump(ctx, h herdr.Herdr, t Target) error                                                // :67  PaneForSession → else os.Stat(worktree) + OpenTab → focus(); retry (500ms interval, 15s timeout; package vars focusInterval/focusTimeout) only when a tab was just opened
func TabLabel(id, title string) string
// prompt.go
func RenderPrompt(tmpl string, PromptData{ID,Title,Description,Acceptance,Worktree,Branch}) (string, error)
```
`Run` steps: render prompt → `Claim` → `DefaultBranch` + `AddWorktree` → optional `File.Setup` via `Shell(wt,"-c",setup)` → CreateSession (30s) → `SetMetadata` (session/worktree/branch) → `Prompt` (30s) → `OpenTab`. Failure before prompt undoes in reverse; tab failure only sets `Result.TabError`. Errors are `"<step>: <err>"` joined, so the failing step name is available for the action error.

### discover (`internal/discover/discover.go`)
```go
type Project struct{ Name string /* Beads db name */; Repo string /* abs, symlinks resolved */ }   // :16
func Scan(roots, hidden []string) ([]Project, []string /*warnings*/)                          // :27  depth ≤4, skips node_modules/vendor/dot-dirs, reads .beads/metadata.json "dolt_database"
func ForDir(projects []Project, dir string) (Project, bool)                                   // :119
```
For `repo: null` resolution, `Scan(roots, nil)` then match `Name == db` (no existing by-name lookup helper; add one).

### config (`internal/config/config.go`)
`Config{Roots, Hidden []string; RereadInterval time.Duration}`, `Default()` (roots `~/gits`), `DefaultPath()` (honours `XDG_CONFIG_HOME`), `Load(path)`. `ProjectFile{Setup, Prompt string}` with `LoadProjectFile(repo)` (reads `repo/.tsk.toml`, zero value if absent), `ExpandHome`. Uses `github.com/BurntSushi/toml`.

## 2. Live state engine today

It is split between `internal/board.Store` (UI-free, already reusable) and message plumbing in `internal/ui/board.go` + `live.go` (Bubble Tea glue).

### Already UI-free: `internal/board/store.go`
```go
type Store struct { now func() time.Time; reachable bool; entries map[key]*entry; snaps map[string]*status.Snapshot }  // :32
type key struct{ repo, id string }; type entry struct{ project; task; display status.Display; since time.Time }
func NewStore(now) *Store
func (s *Store) SetTasks(p discover.Project, tasks []tracker.Task)    // :54 replaces a project's tasks; keeps `since` if display unchanged, else now(); new entries get since=t.UpdatedAt
func (s *Store) SetSnapshot(sessionID string, snap *status.Snapshot)  // :86 nil = failed to load → Unknown; calls recompute()
func (s *Store) SetReachable(ok bool)                                  // :92
func (s *Store) Sessions() []string / SessionsFor(repo) []string       // live() = has tsk.session, not closed, not review
func (s *Store) ProjectForSession(sessionID) (discover.Project, bool)
func (s *Store) Rows(showAll bool) []Row                               // board-view filtering/sorting; not needed by the plugin
```
`derive` (:44): `status.Derive(t.Status, sid, snap)` where `snap` is nil unless `reachable`. `recompute` (:76) resets `since` to `now()` on any display change. This matches the spec's "`since` tracked by tsk since it started (or last activity on first sight)" apart from the first-sight value (`UpdatedAt` of the bead rather than session last activity). Not goroutine-safe ("the Bubble Tea model owns it"); the plugin needs a mutex or a single owner goroutine. `Rows` filters out Untracked/closed — the plugin needs a different enumerator (all entries with their `Display`).

### Glue to replace: `internal/ui/live.go` and `board.go`
Types (`live.go`):
```go
type Live interface { Active(ctx) (map[string]bool, error); Snapshot(ctx, sessionID string, active map[string]bool) (status.Snapshot, error); Root(ctx, sessionID string) (string, error); Subscribe(ctx) (<-chan opencode.Event, error) }   // :16
type snapsMsg struct { snaps map[string]*status.Snapshot; reachable bool; err error }          // :35
func loadSnapshots(ctx, live Live, ids []string) snapsMsg                                       // :47  10s timeout; Active() error → reachable=false; per-id Snapshot error → nil entry
```
`loadSnapshots` is the reusable "full reload" core (rewrite as a plain function returning `(map[string]*status.Snapshot, reachable bool, err error)`). The `Live` interface is a good seam; the plugin can reuse it (and the `fakeLive` pattern).

Event flow (`board.go`, all inside `Update`):
- `Init` (:107): start tick (`reread`, 10s), `loadTasks` for every project, `subscribe()`.
- `subscribedMsg` (:196): on error → `liveErr`, `scheduleReconnect()` (backoff 1s, doubling, cap 30s; reset to 0 on success :201). On success → `waitEvent(ch)` and **reload tasks of all projects**; the tasks handler then calls `loadSnaps(store.SessionsFor(repo))`, which is the "reconnect full reload".
- `waitEvent` (:140) reads one event from the channel; `eventMsg{ev, ch, ok}`. `!ok` (channel closed) → `scheduleReconnect()`.
- `eventMsg` (:207): re-arm `waitEvent`; if `ev.SessionID != "" && opencode.Relevant(ev.Type)` → `resolveRoot(ev)`.
- `resolveRoot` (:147): `live.Root(ctx, ev.SessionID)` (10s); error → drop; else `rootMsg{root, settled: opencode.Settled(ev.Type)}`. This is the parentID tree walk: events from subagent sessions map to the task's top-level session.
- `rootMsg` (:220): `store.ProjectForSession(root)`; unknown root → ignore. If `settled`, `loadTasks(p)` (agent may have run `bd update --status review`). Debounce: `pending[root]` + 300ms `after` → `refreshMsg{id}`.
- `refreshMsg` (:239): `loadSnaps([]string{root})` (one session) → `snapsMsg` → `store.SetSnapshot`, `SetReachable(true)`.
- `snapsMsg` (:181): reachable → `SetSnapshot` for each + `SetReachable(true)`; else `SetReachable(false)` (all sessions → Unknown) and `liveErr` text.
- `tasksMsg` (:172): `store.SetTasks`, then `loadSnaps(SessionsFor(repo))`.
- `tickMsg` (:242): the 10s Beads re-read (to be removed per spec).

Snapshots are recomputed, not incrementally patched: every relevant event triggers a full `Snapshot()` of that root session (BFS over children: `GetSession`, `FormCount`, `PermissionCount`, `Children` per node).

### Extraction plan
Move into a new UI-free package (spec suggests `btplugin`; a separate `live` package would be cleaner):
1. `loadSnapshots` logic (without `snapsMsg`).
2. An event loop goroutine: `Subscribe` → for each event with `SessionID != ""` and `Relevant`: `Root` → debounce 300ms per root → `Snapshot` → `store.SetSnapshot` → emit "changed session X" so the plugin recomputes affected beads. On channel close: backoff 1s→30s, resubscribe, then full reload of `SessionsFor` (all) and `replace: true`.
3. Keep `board.Store` (maybe add `Entries()` / `Display(repo,id)`), or write a leaner store keyed by `(db, id)`; bt gives a database name, `Store` keys by repo path.
4. `Settled` handling: with bt supplying `beads.sync`, the `loadTasks` on settle is replaced by waiting for bt's next sync (the spec drops the 10s reread; `Settled` can be dropped).

Bubble Tea-specific (delete with `internal/ui`): `Board`/`Project` models, all `*Msg` types, `tea.Cmd` wrappers, `After`/`realAfter`, key handling, `View`, `Jumper` type, `ui/format.go`. The `Project` view also keeps its own parallel snapshot map (`project.go:50`) with duplicated logic; it goes away entirely. `Project.dispatch()` (:293) holds the pre-dispatch gates the plugin must reproduce: task must be `ready` (from `Tracker.Ready`) and Queued, or Lost; review status exists (`HasReviewStatus`); `.worktrees` git-ignored (`Git.IsIgnored(repo, ".worktrees/tsk-check")`); OpenCode and herdr available; guards against double-dispatch (`sent` map, `pruneSent` :209). Note "ready" (no open blockers) is a Beads-side concept bt may not tell tsk; spec says `queued` → `[dispatch]`, so decide whether the plugin should still call `Ready`.

## 3. status.State → spec `tsk.state`

| `Derive` result | `tsk.state` | actions | note |
|---|---|---|---|
| `Done` | `state.clear` | | |
| `Untracked` | `state.clear` | | |
| `Review` | `review` | none | |
| `Queued` | `queued` | `[dispatch]` | |
| `Lost` | `lost` | `[dispatch]` | |
| `Waiting` Detail `question` / `permission` / `idle` | `waiting` | `[jump]` | reason is `Display.Detail`; question text must be fetched (see §1, not currently available) |
| `InProgress` | `running` | `[jump]` | |
| `Unknown` | `unknown` | `[jump]` | plus one toast. `Unknown` occurs when OpenCode is unreachable (`reachable=false`) or a single `Snapshot` errored (nil entry) |

Caveat: `Derive` gives `Queued` for any open bead with no session regardless of readiness; `Untracked` covers e.g. `in_progress`/`blocked`/`deferred` beads with no `tsk.session` (cleared). A closed bead with a stale session is `Done` (clear) since the closed check is first.

## 4. cmd/tsk/main.go wiring (`cmd/tsk/main.go`)

- `run(args)` (:37) requires `board` or `project`; ctx from `signal.NotifyContext(Background, os.Interrupt)`.
- `config.Load(config.DefaultPath())` (:44) → roots/hidden/reread. Spec: replace with plugin `options` (`roots`).
- `exec.LookPath("bd")` (:48) hard error.
- `discover.Scan(cfg.Roots, cfg.Hidden)` (:51) at startup only.
- `bd := tracker.New()`.
- `opencode.FromService(ctx)` (:56); on failure append a warning and leave `live` nil (assigned only on success to avoid a typed-nil interface; keep that pattern). The OpenCode service is resolved once at startup; the plugin should retry lazily when it was down at start (and on reconnect).
- herdr: `exec.LookPath("herdr")` (:65) → `herdr.New()`; `jump` closure wraps `dispatch.Jump`.
- Dispatch deps: `dispatch.Deps{Tracker: bd, OpenCode: oc, Herdr: h, Git: gitwt.New(), Shell: execx.Command("sh")}` (:99); `config.LoadProjectFile(p.Repo)` per dispatch.
- Reusable verbatim: the dependency construction (:48-:69, :99); `FromService`; `LoadProjectFile`. To drop: `tea` import, `ui.*`, usage text, `config.Load`/`Hidden`/`RereadInterval`.

## 5. Tests and tooling

- Makefile: `build` (`go build -o bin/tsk ./cmd/tsk`), `install`, `test` (`go test ./...`), `lint` (`go vet ./...` + `gofmt -l`), `e2e` (`go test -tags e2e -count=1 ./e2e/...`).
- e2e: `e2e/dispatch_test.go`, first line `//go:build e2e`; `TestDispatchEndToEnd` builds a throwaway repo with real `bd`, OpenCode service and herdr, and calls `dispatch.Run`.
- Fakes (all unexported, in test files; copy or move to an `internal/testutil` package for `btplugin` tests):
  - `internal/dispatch/dispatch_test.go:20` `recorder` implements `tracker.Tracker`, `gitwt.Git`, `dispatch.OpenCode` and `herdr.Herdr` in one struct, recording calls in order; `deps(r)` at :115.
  - `internal/ui/helpers_test.go:101` `fakeTracker` (tasks/ready per repo, list counts, review status) and :135 `fakeLive` (`ui.Live` fake: snaps, active, parents, `down`, subscribe count; `Subscribe` returns a never-sending channel) and `jumpRecorder`. `fakeLive.Subscribe` cannot inject events; the new tests need a fake that returns a controllable channel.
  - `internal/opencode/client_test.go:20` `world` + `httptest` handler (auth check with `opencode:s3cret-pw`, sessions/forms/permissions/active) is the HTTP-level fake; `events_test.go` tests SSE parsing.
  - `internal/board/store_test.go`, `internal/status/status_test.go` are pure table tests.
- Tests under `internal/ui` die with the package; `board` tests stay.

## 6. What breaks when bt sends beads instead of tsk listing them

1. **Task fields**: `dispatch.Request.Task` is a `tracker.Task`; `Run` uses `Task.ID`, `Title`, `Description`, `Acceptance` (prompt, `dispatch.go:62-65`) and only `ID`/`Title` elsewhere. bt's `beads.sync` does not carry description/acceptance, so the plugin must fetch the full task before dispatch. `tracker` has no `Show`; add `Show(ctx, repo, id) (Task, error)` (`bd show <id> --json`; bd's output is likely an array or object — check) to the `Tracker` interface and update every fake (`recorder`, `fakeTracker`).
2. **Metadata**: state mapping needs `Task.Meta("tsk.session")` (and worktree, branch for `Jump`'s `Target`). Spec says bt syncs `Metadata`? It says bt's hash omits metadata but metadata writes bump `updated_at`; confirm that `beads.sync` payload includes `metadata` (the spec's Part 1 protocol). If not, tsk must `bd show` for every bead with a status other than open/closed, which is expensive. Without a `tsk.session`, `Derive` yields Queued/Untracked.
3. **Project identity**: `status`/`board`/`dispatch` use `discover.Project{Name, Repo}` and `repo` paths as the `bd` working directory. bt sends `db` + optional `repo` (null possible). `repo: null` → `discover.Scan(roots)` and match `Name == db`; if unresolved, omit `dispatch` (`jump` still works if `tsk.worktree` exists, but `Target.Repo` is needed by `OpenTab` → `WorkspaceFor`/`CreateWorkspace`; derive it from the worktree path or skip).
4. **bd invocation**: `execx.Command("bd")` runs with `cmd.Dir=repo`; the spec's review-status check uses `bd -C <repo>`; existing `HasReviewStatus` uses the cwd form and works equivalently once a repo is known. The "first time tsk sees a database" check is a once-per-db toast, whereas today `Project.checks()` is per view; `AddReviewStatus` must not be called (spec: never changes Beads config).
5. **Ready gating**: today's dispatch gate uses `Tracker.Ready`; bt's sync does not say a bead is unblocked. Either call `Ready` at action time or accept dispatch of blocked queued beads.
6. **Missing pre-checks**: `.worktrees` ignore check (`Git.IsIgnored`) lives only in `ui.Project` and must be re-implemented in the action handler (error toast otherwise).
7. **Status values**: `bd` custom status `review` only exists if configured; `Derive` treats any other non-open/closed status without a session as Untracked (cleared), so `blocked`/`in_progress` beads without a session show nothing.
8. **No question text, no herdr-running probe, no `since` persistence** (§1, §2); `board.Store` keys by repo path, not `db`.
9. **Concurrency**: `Store` and `opencode.Client.parents` (mutex-protected) — `Store` is single-threaded by design; the plugin's JSON-RPC reader, event loop and action goroutines need serialization.
10. **Typed-nil interface** trap in `main.go`: keep assigning `live`/`h` only on success.
