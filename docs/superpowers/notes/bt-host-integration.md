# bt host integration notes (read-only investigation)

Repo: `~/gits/beadstui/.claude/worktrees/plugin-slots` (branch `feat/plugin-slots`).
Module path: `github.com/seanmartinsmith/beadstui`. Bubble Tea v2.0.9 (`charm.land/bubbletea/v2`).
All paths below are relative to the worktree.

## 1. Config

- Only reader of `~/.config/bt/config.yaml` is `cmd/bt/root.go:915` `loadBackgroundModeFromUserConfig() (bool, bool)`.
  It builds the path with `os.UserHomeDir()` + `.config/bt/config.yaml` (root.go:921), `os.ReadFile`, then
  `yaml.Unmarshal` (`gopkg.in/yaml.v3`, imported root.go:22) into an anonymous struct:
  ```go
  var cfg struct {
      Experimental struct {
          BackgroundMode *bool `yaml:"background_mode"`
      } `yaml:"experimental"`
  }
  ```
  Every error (no home, missing file, bad YAML) is swallowed and returns `(false,false)`. Pointers distinguish unset from false.
- There is no shared config struct or loader. Add a sibling function (e.g. `loadPluginsFromUserConfig() ([]plugin.Config, error)`) that reads the same path and decodes `Plugins []struct{...} \`yaml:"plugins"\``. Better: put the decode in `pkg/plugin` (`plugin.LoadConfig(path)`) and have both `runRootTUI` and `bt plugins` call it, so they read identical data. Return errors (unlike the bg-mode loader) so `bt plugins` can print them; the TUI path should log via `debug.Log` and continue with no plugins.
- Config does not reach `ui.Model` at all today: the bg-mode value is converted to env var `BT_BACKGROUND_MODE` (root.go:~486-492) and read in `NewModel`. Model configuration is done by pointer-receiver setters called on the value `m` in `runRootTUI` after `ui.NewModel(...)`:
  - `func (m *Model) SetProjectName(name string)` — `pkg/ui/model_modes.go:126`
  - `func (m *Model) EnableWorkspaceMode(info WorkspaceInfo)` — `model_modes.go:149`
  - `func (m *Model) SetCurrentProjectDB(db string)` — `model_modes.go:172`
  - `func (m *Model) SetActiveRepos(repos map[string]bool)` — `model_modes.go:177`
  - `func (m *Model) SetDoltServer(s DoltServerStopper, reconnectFn func(beadsDir string) error)` — `model.go:2056`
  Add the same style of setter, e.g. `func (m *Model) SetPluginHost(h PluginHost)` plus an accessor `func (m Model) SlotRegistry() *slots.Registry { return m.slotRegistry }`. `slotRegistry` is unexported (`model.go:768`, created `model.go:1240-1241`, built-ins via `registerBuiltinSlots`, builtin_slots.go:13). The `*Registry` is shared by pointer with the delegate, board, tree, epics tree, so providers added from outside after `NewModel` are seen without further wiring.

## 2. TUI startup / lifecycle

`cmd/bt/root.go`, `runRootTUI(cmd *cobra.Command)` (root.go:328):
- `--as-of` branch: root.go:454 `m := ui.NewModel(appCtx.issues, activeRecipe, "", nil, appCtx.routeTable)`, `defer m.Stop()` (455), `runTUIProgram(m)` (456).
- Normal branch: root.go:494 `m := ui.NewModel(appCtx.issues, activeRecipe, appCtx.beadsPath, appCtx.selectedSource, appCtx.routeTable)`; setters 497-545 (`SetProjectName`, `SetDoltServer`, global `SetProjectName("global")`+`SetDoltServer(nil, ...)` at 513, `defer m.Stop()` at **529**, `EnableWorkspaceMode` 531-538, `SetCurrentProjectDB`+`SetActiveRepos` 541-544, debug-render exit 548, then `runTUIProgram(m)` at 553).
- `func runTUIProgram(m ui.Model) error` — root.go:792. Takes the Model **by value**. Sets `log.SetOutput(io.Discard)` (794). Builds `teaOpts := []tea.ProgramOption{tea.WithoutSignalHandler()}` (~826), optional `tea.WithFilter(coalescer.Filter)`, then **`p := tea.NewProgram(m, teaOpts...)` at root.go:832**. `coalescer.SetSender(p.Send)` (834). Signal goroutine at ~850 (`p.Quit()` then `p.Kill()` after 5s). **`finalModel, err := p.Run()` at root.go:902**; afterwards `finalModel.(ui.Model)` (value assertion) is used only for `fm.DoltShutdownMsg()`.
- `Model` is a **value** type through the program: `func (m Model) Init() tea.Cmd` (`pkg/ui/model.go:1729`), `func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)` (1773), returns `Model` values. Anything that must survive copies must live behind a pointer (worker, watcher, registry, events ring).
- `func (m *Model) Stop()` — `model.go:2030`. Stops `m.data.backgroundWorker`, `m.data.watcher`, releases `instanceLock`, returns pooled issues, stops Dolt server if owned. It is invoked on the **original local `m`** in `runRootTUI` via `defer`, not on the final model; that works because everything it stops is pointer-held. A plugin host must therefore be a pointer (`*plugin.Host`) stored in the model and stopped from `runTUIProgram`/`runRootTUI`, not through the final model value.
- Recommended hook points: construct host in `runRootTUI` after the setters (before 553) and in the `--as-of` branch only if desired (probably skip: no live data). Simplest is in `runTUIProgram` right after `p := tea.NewProgram(...)`: `host.SetSender(p.Send)` (mirrors `coalescer.SetSender(p.Send)` at 834), `host.Start()`, `defer host.Stop()` (alongside `defer close(runDone)`, so it runs after `p.Run()` returns; the signal goroutine already routes SIGINT to `p.Quit()`). Because `runTUIProgram` takes only `m`, either give it a second param or have the host pointer stored in the model via a setter and exposed with an accessor.

Async message injection patterns in the repo:
1. `p.Send(msg)` — used by `ResizeCoalescer` (`pkg/ui/resize_coalesce.go`: `SetSender(send func(tea.Msg))`, `RunFlushPump(stop <-chan struct{})`). `Program.Send` (bubbletea tea.go:1192) is `select { case <-p.ctx.Done(): case p.msgs <- msg: }`: safe to call from any goroutine, never blocks forever after the program exits, but can block while the loop is busy.
2. Channel + re-subscribe Cmd: `WaitForBackgroundWorkerMsgCmd(w)` (`model.go:374`) blocks on `<-w.Messages()` / `<-w.Done()` and returns the msg; every handler must return the Cmd again or the chain dies silently (comment at model.go:1845-1849). Init subscribes at model.go:1735-1737.
**Recommendation:** use `p.Send` via a sender func set after `tea.NewProgram` (pattern 1). It needs no handler re-subscribe discipline, and plugin messages can be sent straight from the per-plugin reader goroutines. Keep message types exported from `pkg/ui` (or `pkg/plugin`) and add cases to the `switch msg := msg.(type)` in `Update` (the worker cases are at model.go:1841-1871). For actions *from* the UI, return a `tea.Cmd` that does the blocking RPC and yields a result msg (same shape as `writeCmd`/`writeResultMsg`, claim.go).

## 3. First snapshot / data changes

Two paths replace the issue set:
- Worker path: `func (m Model) handleSnapshotReady(msg SnapshotReadyMsg) (Model, tea.Cmd)` — `pkg/ui/model_update_data.go:26`. `firstSnapshot := m.data.snapshotInitPending && m.data.snapshot == nil` (line ~60). Swap at ~99 `m.data.snapshot = msg.Snapshot`; **`m.data.issues = msg.Snapshot.Issues` (118), `m.data.issueMap = msg.Snapshot.IssueMap` (119)**. The handler ends (line ~406) with `WaitForBackgroundWorkerMsgCmd` re-subscribe. Snapshots arriving while a modal is open are deferred and re-emitted (shouldDeferRefresh, model.go:200), so the handler can run late.
- Non-worker path (Dolt poll / JSONL reload): `handleDataSourceReload(msg DataSourceReloadMsg)` — model_update_data.go:437 → `m.replaceIssues(msg.Issues)` which sets `m.data.issues` (model_update_data.go:694) and `m.data.issueMap` (715). A second similar setter exists at `model.go:1629/1637`. Initial `NewModel(issues, ...)` populates the same fields; when `backgroundWorker == nil` there is no `SnapshotReadyMsg` ever, so the host must also be fed from `Init()`/first `Update`, not only from `handleSnapshotReady`.
- Hash: `DataSnapshot.DataHash string` (`pkg/ui/snapshot.go:170`), set in the worker at `background_worker.go:1543` `hash := analysis.ComputeDataHash(issues)` → `snapshot.DataHash = hash` (1637). Signature: `func ComputeDataHash(issues []model.Issue) string` (`pkg/analysis/cache.go:138`; sha256 over sorted issues incl. `SourceRepo`; returns `"empty"` for none). Model access: `m.data.snapshot.DataHash` (nil snapshot when no worker). For the non-worker path compute it yourself with `analysis.ComputeDataHash(m.data.issues)` (cost O(n log n), acceptable once per reload).
- Suggested integration: add a small `func (m *Model) notifyPluginHost(cmds *[]tea.Cmd)` called at the end of `handleSnapshotReady` (just before the final `return` ~406) and `handleDataSourceReload` (~473); it compares the hash with `m.pluginLastHash` (copy-safe string field) and, if changed, sends `beads.sync` from a goroutine/Cmd with a copy of the issues (the slice is owned by the model; `pooledIssues` are returned to a pool asynchronously — `go loader.ReturnIssuePtrsToPool(oldSnapshot.pooledIssues)` at ~110 — so **copy/serialize the needed fields synchronously in the handler**, never hold `*model.Issue` pointers across the Cmd).
- Rendering after plugin state changes: list/board/tree/epics rows call `registry.Badges` at render time, so a no-op re-render (any `Update` return) suffices. Detail-pane sections are baked into the viewport string by `updateViewportContent` (the `m.slotRegistry.Sections(...)` call is at `pkg/ui/model_filter.go:1089`), so after a plugin state update call `m.updateViewportContent()` for the selected bead. BQL fields are evaluated per execution (`slot_bql.go:22`), no refresh needed (an already-applied BQL filter will not re-run until `m.reapplyActiveFilter()`).

## 4. Bead identity

`model.Issue` (pkg/model/types.go): `ID string`, `SourceRepo string \`json:"source_repo,omitempty"\`` (types.go:42).
- Global mode: `issue.SourceRepo = globalSource` — the Dolt database name (`internal/datasource/global_dolt.go:772`, comment at 771 "SourceRepo always comes from the database name in global mode"). Authoritative.
- Project (server) mode: `SourceRepo` is only whatever the `source_repo` column holds (`internal/datasource/dolt.go:275`, `if sourceRepo.Valid`) — a bd field, not guaranteed to be the DB name (do not rely on it). Workspace file mode (`.bt/workspace.yaml`): `workspace.AggregateLoader` namespaces IDs with a prefix; I did not verify what it puts in `SourceRepo`, so treat db as the repo `Prefix`.
- Project DB name source: `appCtx.currentProjectDB` is set **only in global mode** (root.go:1065 in `loadGlobalMode`; copied to the model by `SetCurrentProjectDB`, `Model.currentProjectDB` at model.go:692). For project mode there is no DSN parsing helper in the repo; the DB name comes from metadata: `func detectCurrentProjectDB() string` (root.go:1301, cwd) / `func detectProjectDBAt(dir string) string` (root.go:1324) → `bdroute.ReadMetadataAt(dir) (ProjectMetadata, error)` (`internal/bdroute/bdroute.go:205`, `ProjectMetadata{DoltDatabase, DoltMode, ProjectID}`; honours `BEADS_DIR` and git-worktree walk-up). `DataSource.Path` for Dolt is a DSN (`internal/datasource/source.go:86-103`; `DoltConfig.DSN()` embeds `Database`, metadata.go:23), but parsing it is unnecessary.
- Concrete function (put in `cmd/bt` or pass a closure into the host):
  ```go
  func beadDB(projectDB string, global bool) func(model.Issue) string {
      return func(i model.Issue) string {
          if global && i.SourceRepo != "" { return i.SourceRepo }
          if projectDB != "" { return projectDB }          // detectCurrentProjectDB()
          return i.SourceRepo                               // workspace/legacy fallback
      }
  }
  ```
  where `global = appCtx.selectedSource != nil && appCtx.selectedSource.Type == datasource.SourceTypeDoltGlobal`. Note an auto-global launch from inside a project has `currentProjectDB != ""` AND global source, so SourceRepo must win when set (as above).
- Checkout path: `func (t *Table) Resolve(issue model.Issue) (WriteTarget, error)` (bdroute.go:135); `type WriteTarget struct { Dir string; Global bool }` (53). Mode-specific:
  - single-project (`bdroute.SingleProject(cwd)`, root.go:293/372): always `{Dir: cwd}`, never errors.
  - workspace (`FromWorkspace`, root.go:206): key = prefix before first `-` of `issue.ID`; error if unmapped.
  - global (`FromSettings`, root.go:1078 `buildGlobalRouteTable`): requires `SourceRepo`; refuses `beads_global` (`BeadsGlobalDB`), unknown/stale/embedded checkouts (re-reads `.beads/metadata.json` each call). Errors are human-readable but worded for "claim"; the plugin host should wrap them, not show raw.
  The Model holds the table as `routeTable *bdroute.Table` (model.go:1032, set model.go:1608); `Resolve` is cheap enough for per-sync use but does file IO in global mode, so call it lazily (on action or when building the snapshot for a plugin that declared it needs `path`) and cache per db.

## 5. Scope mode

No explicit mode enum on Model; `bdroute.Table.mode` is private with no accessor. Derive:
- Global (shared server): `m.data.dataSource != nil && m.data.dataSource.Type == datasource.SourceTypeDoltGlobal` (`datasource.SourceTypeDoltGlobal`, source.go:74). Also `isDoltSource()` (model.go:1693, covers Dolt+global), `isEmbeddedSource()` (model.go:1702). Launch-side: `flagGlobal` or `autoGlobalWithColdBoot` (root.go:~1000-1060) → `appCtx.selectedSource.Type`.
- Workspace flag: `m.workspaceMode bool` (model.go:688, getter `IsWorkspaceMode()` model_modes.go:163). **It is also true in global mode**, because `appCtx.workspaceInfo = buildWorkspaceInfoFromIssues(result)` (root.go:311) is passed to `EnableWorkspaceMode` (root.go:531). So `.bt/workspace.yaml` mode = `workspaceMode && (dataSource == nil || Type != DoltGlobal)`.
- Project mode: neither (`appCtx.workspaceInfo == nil`).
- Databases in global mode: `m.availableRepos []string` (model.go:689; normalized lowercase and trimmed of `-:_` by `normalizeRepoPrefixes`, `pkg/ui/workspace_repos.go:11`, so **not the raw DB names**). Raw names: distinct `issue.SourceRepo` (what `buildWorkspaceInfoFromIssues` does, root.go:311-325) or `datasource.EnumerateDatabases(db *sql.DB, repoFilter string) ([]string, error)` (global_dolt.go:171) / `(*GlobalDoltReader).Databases()` (global_dolt.go:451). `m.activeRepos map[string]bool` (nil = all) is the repo-picker filter.
- Preferred: compute `scope` (project|global|workspace) and `dbs` once in `cmd/bt` from `appCtx` and give the host a plain struct; avoid re-deriving inside `pkg/ui`.

## 6. Redraw coalescing

- No generic debounce helper. Patterns in use:
  - `tea.Tick` self-rearming chains: `workerPollTickCmd()` 120ms (model.go:345), `writeSpinnerTickCmd()` 150ms with `writeSpinnerActive` flag + `writeSpinnerIdx++` and self-cancel when `len(m.pendingWrites)==0` (`pkg/ui/claim.go:86-117, 290-301`; armed at claim.go:202-204 and field_edit.go:569-571), `statusTickCmd()` 1s (`model_footer.go:129`). The write-spinner shape is the one to copy for the plugin "pending" spinner: bool `active` field, tick msg, handler that bumps an index, calls `m.updateListDelegate()`, re-arms only while work is pending. Frames: `claimSpinnerFrame(idx)` (claim.go:107) → `workerSpinnerFrames`.
  - Debounce by timestamp: semantic search compares `time.Since(lastQueryTime) >= 150ms` else schedules `tea.Tick(150ms)` (model.go ~2010-2020).
  - Goroutine coalescer with `p.Send`: `ResizeCoalescer` (resize_coalesce.go) — stash latest, ticker pump flushes after quiet window.
- For "redraw at most every 100ms": simplest is host-side — reader goroutines mark `dirty` (atomic) and a single 100ms `time.Ticker` goroutine does `if dirty.Swap(false) { send(pluginStateChangedMsg{}) }`. Bubble Tea re-renders after every Update, so the msg handler can be a no-op (plus `updateViewportContent()` for the selected bead). Stop the ticker in `host.Stop()`.

## 7. Toasts / notifications

All on `*Model` in `pkg/ui/model_footer.go`; fields `statusMsg`, `statusSeverity`, `statusIsInline`, `statusSetAt`, `statusSeq`:
- `setInlineTransientStatus(msg string, d time.Duration) tea.Cmd` (29): success-severity toast bubble that auto-clears after `d` via `statusClearMsg{seq}`; **returns a Cmd that must be batched**.
- `setStatus(msg)` (42): success/info, ~3s via idle tick (`statusDismissAge`), no Cmd.
- `setNotice(msg)` (60): rejection/validation, ~3s, no events entry. Closest to "warn".
- `setFailure(msg)` (69): error, ~8s, also appends `events.NewSystemEvent(msg)` to `m.events` (ring buffer, alerts modal).
- `setDegraded(msg)` (81): sticky until `clearStatus()` (93); also logged to events.
- `setStatusError(msg)` (52): temporary alias for failure severity, no events entry.
Mapping for `ui.toast` level: info → `setStatus`, warn → `setNotice`, error → `setFailure`. `handleKeyPress` clears `statusMsg` on any keypress (model_update_input.go:42-52), so a toast set from a key-triggered action can vanish immediately if set before the keypress finishes; set it from the async result msg handler. The events ring is `m.events *events.RingBuffer` (model.go:896; `pkg/ui/events`: `NewSystemEvent(summary string) Event`, `(*RingBuffer).Append/AppendMany`).

## 8. Existing subprocess / RPC style, logging

- No JSON-RPC anywhere (`grep -ri jsonrpc` → nothing). No long-lived stdio child process either; all subprocess use is run-to-completion:
  - `internal/bdexec/bdexec.go`: `type Result struct{Args []string; ExitCode int; Stdout, Stderr string; Duration time.Duration; Err error}`, `func Run(ctx context.Context, dir string, args ...string) Result`, `RunWithTimeout(...)`; uses `exec.CommandContext`, buffers stdout/stderr, inherits env deliberately, `DefaultTimeout = 30s`. Tests swap seams via package vars (`claimRunner`, claim.go:~97).
  - `internal/doltctl/doltctl.go:103,180` (`bd dolt start/stop` via `exec.CommandContext` with ctx timeout); `pkg/hooks` runs trusted export hooks (see trust DB `~/.bt/hook-trust.json`, `cobra_hooks.go`); `pkg/cass/*.go:193,239`.
  Follow that style: package-level function-typed seams for spawning (e.g. `var startProcess = exec.CommandContext`), `context` for lifetime, errors returned not logged, no globals in `pkg/ui`.
- Logging: `pkg/debug/debug.go` — enabled only if env `BT_DEBUG` is set; `func Log(format string, args ...any)` (58) writes to **stderr** with `[BT_DEBUG]` prefix; also `LogIf`, `LogTiming`, `Enabled()`. The std `log` package is silenced during the TUI (`log.SetOutput(io.Discard)`, root.go:794). So plugin stderr lines should go through `debug.Log("plugin %s: %s", name, line)` only when `debug.Enabled()` (that would draw on the alt screen if BT_DEBUG is set — existing behaviour of debug), and otherwise be kept in a bounded in-memory ring per plugin (for `bt plugins` status / an error toast). Do not write to os.Stderr directly. Always drain the child's stderr or the child can block.

## 9. Cobra subcommands

- Pattern (`cmd/bt/cobra_hooks.go`): package-level `var hooksCmd = &cobra.Command{Use, Short, Long}`, children `hooksListCmd` with `Args: cobra.MaximumNArgs(1), RunE: runHooksList`, and an `init()` at the bottom (186-188): `hooksCmd.AddCommand(...)`, `rootCmd.AddCommand(hooksCmd)`. Create `cmd/bt/cobra_plugins.go` the same way (`pluginsCmd`, `RunE: runPlugins`).
- Root command (root.go:81-94): `SilenceUsage/SilenceErrors: true`, `PersistentPreRunE: rootPersistentPreRun` (root.go:137) only validates flags; **it does not call `loadIssues()`**. Each subcommand that needs data calls `loadIssues()` itself in its own `PersistentPreRunE`/`RunE` (e.g. cobra_baseline.go:24, cobra_misc.go:17), so `bt plugins` costs no Dolt connection if it doesn't call it. Persistent flags `--global`, `--workspace`, `--repo`, `--format` still apply.
- Use `fmt.Printf` for output, return `error` from `RunE` (printed by main since errors are silenced — check `main()` in cmd/bt for the exit-code path before relying on it).
- Config: call the same `plugin.LoadConfig(path)` used by the TUI. For status without starting anything: print name, command, resolved binary (`exec.LookPath`), enabled/valid. For `--probe`: `host.Start()` with a 2s timeout, wait for `initialize` reply, print capabilities, `Stop()`.

## 10. Test patterns (pkg/ui)

- Construct: `ui.NewModel(issues []model.Issue, activeRecipe *recipe.Recipe, beadsPath string, ds *datasource.DataSource, routeTable *bdroute.Table) Model` (model.go:1160). Typical in-package helper (`pkg/ui/claim_test.go:41-48`):
  ```go
  func newSizedModelWithRoute(t *testing.T, issues []model.Issue, w, h int, table *bdroute.Table) Model {
      m := NewModel(issues, nil, "", nil, table)
      updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
      return updated.(Model)
  }
  ```
  `newSizedModel(t, issues, w, h)` wraps it with `bdroute.SingleProject(t.TempDir())`. `Update` is a **value** receiver returning `tea.Model` → assert `updated.(Model)`. Handlers like `handleSnapshotReady` can be driven directly: `m.Update(SnapshotReadyMsg{Snapshot: snap, SentAt: time.Now()})` (claim_integration_test.go:177).
- Key input: `tea.KeyPressMsg{Code: 'm', Text: "m"}`.
- teatest **is** used: `github.com/charmbracelet/x/exp/teatest/v2` (go.mod:19), e.g. `claim_test.go:455` `teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 32))`, `teatest.WaitFor(t, tm.Output(), func(b []byte) bool {...}, teatest.WithDuration(8*time.Second))`, `tm.Send(...)`, `tm.Quit()`. teatest runs its own Program, so a host would need a fake sender; for host tests prefer a `Sender func(tea.Msg)` field set to a recording func and a fake plugin binary (re-exec `os.Args[0]` with env var `GO_WANT_PLUGIN_HELPER=1`, the usual Go idiom).
- Slots tests: unit-test providers directly (`pkg/ui/builtin_slots_test.go` calls `capabilitySections(issue, slots.Context{...})`); `pkg/ui/slots` has its own tests of `Registry`. External-package tests (`package ui_test`, helpers_test.go) can only use exported API, so add exported setters/accessors (`SlotRegistry()`, `SetPluginHost`) if the host tests live there.

## Gotchas summary

1. `Model` is copied by value on every Update: keep the host/registry behind pointers; mutate model state only inside `Update` handlers.
2. Providers are called on the render path: `Registry` reads must not block; the plugin-state provider should read an `RWMutex`-guarded map keyed by `(db, id)`.
3. `data.issues` may be backed by pooled structs returned asynchronously; copy before leaving the Update goroutine.
4. Project-mode DB name = `detectCurrentProjectDB()`, not `SourceRepo`; global mode = `SourceRepo`.
5. `workspaceMode` is also true in global mode; use `dataSource.Type`.
6. Toasts set during `handleKeyPress` are cleared by the same keypress; set them from the async result handler.
7. Stderr from `debug.Log` is the TUI's own terminal: don't spam it from plugin readers.
