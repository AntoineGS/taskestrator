# bt plugin actions: integration notes

Read-only survey of `beadstui/.claude/worktrees/plugin-slots` (branch with the slots work merged). All paths are relative to that worktree. Line numbers are as of this survey.

## 1. Key dispatch order (`pkg/ui/model_update_input.go`, `handleKeyPress`, line 42)

Order of checks, top to bottom:

1. L50-52: clears `statusMsg` on every key press. Toasts set by a key handler survive; toasts set earlier are wiped.
2. L59: if `focused != focusList`, `commitFilterIfTyping()`.
3. L64-659: one `if m.activeModal == ModalX { ...; return }` block per modal (AgentPrompt, CassSession, EpicCard, Update, LabelHealthDetail, LabelDrilldown, LabelGraphAnalysis, Alerts, RepoPicker, BQLQuery, LabelPicker, RecipePicker, Settings, SettingsMenu, QuitConfirm L595, ClaimConfirm L608, FieldSelect/FieldPicker/FieldInput L629-646, LongformEdit L654). **New modals go here**, after L646-659 and before Help (L662). Each block handles `ctrl+c` itself with `return m, tea.Quit`.
4. L662-748: globals that work in any state: Help, Tutorial, Refresh, ctrl+p, Sidebar. L751 sidebar scroll. L768 HybridPreset and L797 SearchMode (list only).
5. L880 `focusHelp`, L886 `focusTutorial`, L900 `focusTimeTravelInput` (typing; returns early).
6. L918-939, `if m.activeModal == ModalNone`: input-capturing sub-states route to their handler and return: history search/file tree, **`m.mode == ViewBoard && m.board.IsSearchMode()`**, memories search.
7. L945 SearchBounce `/`, L962 `Global.Quit` (ctrl+c).
8. **L967 `if m.list.FilterState() != list.Filtering {`**
   - L972-1516: `switch { case key.Matches(msg, m.keys.Global.X): ... }`, the global view-switch cascade (Back `q`, Cancel `esc`, b g a T i p h f ! 1 : l w W ctrl+w x E ... L1495 LabelPicker). These return.
   - **L1518 `switch m.focused {`** focus-specific handlers: `focusBoard` L1540 `handleBoardKeys`, `focusTree` L1585 `handleTreeKeys`, `focusEpics` L1594 `handleEpicsKeys`, `focusList` L1603 `handleListKeys`, `focusDetail` L1606 (forwards a whitelist of ListNormal keys to `handleListKeys`, else `viewport.Update`).
9. L1646: `return m, tea.Batch(cmds...)`. Back in `Update` (`pkg/ui/model.go` ~L1925), when `focused == focusList && activeModal == ModalNone` the key is also forwarded to the bubbles list. A handler that consumes a key must `return` before this.

Constraint: `handleListKeys`, `handleBoardKeys`, `handleTreeKeys`, `handleEpicsKeys` all have signature `(Model, KeyMsg) Model` and **return no `tea.Cmd`**. The plugin dispatch cannot live inside them.

### Where to insert

Insert a new block between L1516 (end of the global `switch`) and L1518 (`switch m.focused`):

```go
if m2, cmd, ok := m.tryPluginActionKey(msg); ok { return m2, cmd }
```

Why this location:
- Already inside `FilterState() != list.Filtering`, so list-filter typing is excluded.
- After modals, TimeTravelInput, board search, and the global cascade, so none of those can be shadowed or interrupted.
- It still precedes the per-view handlers, so it must refuse keys the view binds itself.

Guards (all inside `tryPluginActionKey`):
- `m.activeModal == ModalNone`.
- View/focus: `(m.mode == ViewList && (focused == focusList || focused == focusDetail))`, `(ViewBoard && focusBoard && !m.board.IsSearchMode())`, `(ViewTree && focusTree)`, `(ViewEpics && focusEpics)`.
- Not shadowing: the view's own bindings win. Check `key.Matches(msg, <every binding in the view map>)` using `viewSpecificKeyMap().FullHelp()` flattened (plus `ListNormal.FilterOpen/Closed/Ready/CycleStatusFilter` for board, which `handleBoardKeys` reuses at board_keys.go L131-166). Better: reject plugin action keys at registration if they collide with any key in `Global` or the view map, and keep this dispatch-time check as a safety net.
- The bubbles list also consumes `b u h left pgup` (prev page), `l f d right pgdown` (next page), `g home`, `G end` (`charm.land/bubbles/v2@v2.1.1/list/keys.go` L46-58). So lowercase `d/f/b/u` are not usable in the list even though no bt map binds `d`. Uppercase `D`/`J` are unbound everywhere (see section 2).

"Typing" states to exclude (all already excluded at the insertion point except where noted):
- list filter: `m.list.FilterState() == list.Filtering`
- board search: `m.board.IsSearchMode()` (handled earlier at L926)
- BQL modal: `activeModal == ModalBQLQuery` / `focusBQLQuery`
- time-travel prompt: `focusTimeTravelInput`
- label/repo picker search: modal blocks above
- history/memories search: L920, L932 (not our four views)
- FieldInput, LongformEdit: modal blocks above

## 2. Is `v` free?

No. `v` is bound in **Epics** (`pkg/ui/keys/epics.go` L67: `Card`, "zoom card", used in `epics_view.go` `handleEpicsKeys` `k.Card`) and in History (`keys/history.go` L84). It is unbound in Global, ListNormal, BoardNormal and Tree. `V` is `ListNormal.CassSession`.

Explicit keys per map (Global + view) and resulting free lowercase letters (excluding bubbles list/viewport builtins):

| View | Used lowercase | Free |
|---|---|---|
| list | a b c e f g h i j k l m o p q r s t u w x y (+ builtin d) | d (builtin page), n, v, z |
| board | a b d f g h i j k l n p q s u w x y z (+ o c r from ListNormal filters) | e, m, t, v |
| tree | a b f g h i j k l o p q u w x | c d e m n r s t v y z |
| epics | a b f g h i j k l p q s u v w x z | c d e m n o r t y |

No lowercase letter is free in all four views (`m` is free in board/tree/epics but is `ListNormal.Claim`; `e` is `ListNormal.FieldEdit`). Use uppercase for plugin actions. Verified by grep of `key.WithKeys` in `pkg/ui/keys/*.go`: `D` is bound nowhere; `J` is bound only in History (`keys/history.go` L64), so both `D` and `J` are free in list, board, tree and epics. Still run a collision check at registration (uppercase used somewhere: A C E F G H J K L N O R S T U V W X).

For the menu key: `v` works in list, board, tree, but collides in epics. Options: (a) rebind epics `Card` to another key; (b) pick a menu key free in all four, e.g. `ctrl+a`-style chord (not used) or `space`; (c) in epics, check `Card` first and let `v` fall through to Card (menu unreachable there). Recommend (b) or a free uppercase letter, otherwise (a) with a changelog note.

## 3. Selected bead per view

Focus enum (`pkg/ui/model.go` L59-90): `focusList` (iota), `focusDetail`, `focusBoard`, `focusGraph`, `focusTree`, ..., `focusEpics`. View mode: `ViewList, ViewBoard, ViewTree, ViewEpics, ...` (L94-107).

- List: `m.list.SelectedItem().(IssueItem)` → `.Issue.ID` (pattern at `claim.go` requestClaim; helper `m.selectedIssueID()` at model_update_input.go L2807 does exactly this).
- Board: `m.board.SelectedIssue() *model.Issue` (`board.go` L829, nil when empty column).
- Tree: `m.tree.SelectedIssue() *model.Issue` (`tree.go` L758, nil if none).
- Epics: `m.epicsTree.cursorRow() (epicTreeRow, bool)` (`epics_tree.go` L293). `epicTreeRow.kind` is `rowProjectHeader | rowEpic | rowChild`; `.issue` is nil for `rowProjectHeader` (L65), so no-op there. `cursorEpicID()` (L347) returns the epic ID (parent epic for a child row), not what we want for children.

`focusDetail` should count as list: the detail pane shows `m.list.SelectedItem()` and the existing code treats selection as shared (comment at model_update_input.go L1134-1136). The `focusDetail` case already forwards claim/edit keys to `handleListKeys` (L1631-1640). Gate on `m.mode == ViewList` with focused in {focusList, focusDetail}. Resolve the bead through `m.data.issueMap[id]` for routing, like `confirmClaim` does.

## 4. Pending spinner (`pkg/ui/claim.go`)

- `writeKind` (L41): `writeClaim`, `writeFieldEdit`. Add `writePluginAction`.
- `pendingWrite{Kind, Field, Target, StartedAt}` (L53). `Model.pendingWrites map[string]pendingWrite` keyed by issue ID; initialized in `NewModel` (model.go L1604). `Field` is used in the timeout toast via `label()` (L64): store the action label there.
- `writeResultMsg{id, kind, field, result bdexec.Result}` (L76) is bd-specific; add a separate `pluginActionResultMsg` and a `case` next to `case writeResultMsg:` (model.go L1885).
- Spinner: `writeSpinnerTickMsg` (L86), `writeSpinnerTickCmd()` (L116), `writeSpinnerInterval = 150ms`, `m.writeSpinnerActive` guard, `handleWriteSpinnerTick` (L293) increments `writeSpinnerIdx` and calls `updateListDelegate()`; it stops itself when `len(pendingWrites) == 0`. Start pattern (confirmClaim): 
  ```go
  cmds := []tea.Cmd{...}
  if !m.writeSpinnerActive { m.writeSpinnerActive = true; cmds = append(cmds, writeSpinnerTickCmd()) }
  ```
- `pendingWriteIDs()` (L306) → `map[string]bool`, passed to `IssueDelegate.PendingClaims` + `ClaimSpinner: claimSpinnerFrame(m.writeSpinnerIdx)` in `updateListDelegate` (`semantic_search.go` L735-748). The delegate swaps the 2-cell selection caret for the spinner (`delegate.go` L282).
- 45s timeout: `writeSettleTimeout` (L123). Enforced only in `settlePendingWrites` (L401), called from the data reload paths (`model_update_data.go` L368, L466). So the timeout only fires on a reload. `writeSettled` (L328) returns `false` for unknown kinds, so a plugin entry is cleared only by timeout unless handled.
- **Board/tree/epics do not show the spinner.** `PendingClaims`/`pendingWrites` are referenced only by delegate.go and semantic_search.go. Row renderers: board card line 1 at `board.go` L1405-1442, tree `renderNode` (`tree.go` L570-620; indicator is the expand glyph at L584), epics `renderChildRow` (`epics_tree.go` L772+, glyph from `statusGlyph`).

Minimal design:
1. On action start: set `m.pendingWrites[id] = pendingWrite{Kind: writePluginAction, Field: actionLabel, StartedAt: now}`, `m.updateListDelegate()`, start the tick as above. Refuse with `setNotice` if already pending (same as `requestClaim`).
2. Clear on (a) result msg: `delete(m.pendingWrites, id)`; `updateListDelegate()`. If the plugin result is "ok but async", clear anyway on the result msg (the plugin owns its state; bt cannot target-compare).
3. Clear on (b) plugin state push for that bead: in the handler for the push, `delete(m.pendingWrites, id)` when `Kind == writePluginAction`; then `updateListDelegate()`.
4. Clear on (c) timeout: add `case writePluginAction:` in `writeSettled` returning `false`, and rely on the 45s branch in `settlePendingWrites`. Because that only runs on a data reload, also expire in `handleWriteSpinnerTick` (L293): loop and drop entries older than `writeSettleTimeout` with `setFailure`. Do this check there so it fires without a reload; the tick already runs while anything is pending.
5. Show it in the other views: pass `m.pendingWriteIDs()` + `claimSpinnerFrame(m.writeSpinnerIdx)` into `BoardModel`, `TreeModel`, `EpicsTreeModel` (like `SetSlots`) and prefix the first cell with the frame. Epics caches `m.epicsViewText = m.epicsTree.View()`, so `handleWriteSpinnerTick` must refresh it when `m.mode == ViewEpics`. Smaller alternative: v1 shows the spinner only in the list and a `setNotice("<label> <id>...")` elsewhere (this is what `confirmClaim` does with `setNotice("Claiming %s...")`).

Also: `shouldDeferRefresh()` (model.go L199) defers data reloads while any modal is open, so settle does not run under an open modal.

## 5. Modals

Registry: `ModalType` iota (`model.go` L135-166; append new constants at the end, after `ModalSettingsMenu`, to avoid renumbering). `m.activeModal`, `openModal(t)` L211, `closeModal()` L214. Focus enum entries (`focusClaimConfirm` L85) are set alongside, and mapped in `model_modes.go` L433-435 (`focusName` style switch); add a name for each new focus.

Checklist for a new modal (the claim confirm touches exactly these places):
1. `model.go`: add `ModalPluginConfirm`, `ModalPluginSelect` (append at end of const block, L165) and `focusPluginConfirm`, `focusPluginSelect` (L89 area; focus iota order is not persisted, but add before `focusMemories` is fine).
2. `model.go` Model struct (~L1014-1025): fields for the pending request, e.g. `pluginPrompt *pluginPrompt` holding request id, bead id, title, message, labels, options, cursor.
3. `model_modes.go` ~L433: `case focusPluginConfirm: return "plugin_confirm"` etc.
4. `model_update_input.go`, new early-return block after L646/659 (same shape as ClaimConfirm L608):
   ```go
   if m.activeModal == ModalClaimConfirm {
       switch msg.String() {
       case "ctrl+c": return m, tea.Quit
       case "y", "Y", "enter": return m.confirmClaim()
       default: m.cancelClaim(); return m, nil
       }
   }
   ```
   Confirm: `y/Y/enter` → answer true; `n/N/esc` (and any other key, as claim does) → answer false. Select: use `key.Matches` against a new `keys.PluginSelectKeys` (clone of `FieldPickerKeys`: Up `up,k`, Down `down,j`, Apply `enter`, Cancel `esc`; see keys/field_edit.go L128-168 and `handleFieldPickerKeys` field_edit.go L618). Both close the modal, set `m.focused` back to the originating focus (claim hard-codes `focusList`; for board/tree/epics restore the real focus, store it in the prompt struct), and return a `tea.Cmd` that delivers the answer to the plugin (send on a channel / call the plugin client) and resolves to the action result msg.
5. `model_view.go`: in the `activeModal` switch (L125-150) add `case ModalPluginConfirm:` with the "Handled as overlay after background renders" comment, and at the bottom (next to L335) add:
   ```go
   if m.activeModal == ModalClaimConfirm {
       body = OverlayCenterDimBackdrop(body, m.renderClaimConfirm(), m.width, m.height-1)
   }
   ```
   for each new modal (per `docs/design/tui-modal-compositing.md`: expose content as a `View()`/render method, no centering; use `OverlayCenterDimBackdrop(body, panel, m.width, m.height-1)`; never plain `OverlayCenter`).
6. Rendering template: `renderClaimConfirm` (`claim.go` L443-506): lipgloss styles from `m.theme`, `maxInner := m.width - 8` (min 16), `truncateRunesHelper`, `centerLine`, `RenderTitledPanel(content, PanelOpts{Title, Width: innerW+2+sidePad, CenterTitle: true, BorderColor: t.Primary, TitleColor: t.Primary, Focused: true})`. Reuse the helper **`renderFieldModalLines(title string, lines []string, theme Theme, maxInner int) string`** (`field_edit.go` L59) which already wraps that box math. Select list lines: copy `FieldPickerModal.View` (field_edit.go L301-332): `"> "` cursor with `t.Primary` bold, label + dim description, hint line `"j/k move  enter select  esc cancel"`.
7. Footer/sidebar map: `modalKeyMap()` (`model_footer.go` L857) returns `nil` for unknown modals (empty L1 hint, empty sidebar). Add cases for the select modal's keys; confirm can stay `nil` like `ModalClaimConfirm`.
8. Keys: new `PluginSelectKeys` needs `AppKeys.PluginSelect` (`keys/keys.go`), constructor in `NewAppKeys`, and an entry in `allMaps()` in `keys/keys_test.go` (L40-55), else `TestAllMapsRegistered_MatchesAppKeys` fails; every binding needs a non-empty `Help.Desc` (`TestAllBindings_HaveHelpDesc`).
9. (c) The action menu is a select modal: options = actions applicable to the focused bead, `label` + key hint as description. Enter dispatches the same function the direct key press uses. Needs no extra modal type; set a flag `kind: menu` on the prompt so the answer routes locally rather than to the plugin.

Other modal-touching spots: `context.go` L60 (maps modal to help context; has a `ModalQuitConfirm` case), `handleMouseClick` (`model_update_input.go` L1718: any non-None modal swallows clicks, nothing to do).

## 6. Toasts

All on `*Model` in `pkg/ui/model_footer.go`:
- `setStatus(msg string)` L42, success/info, ~3s fade.
- `setNotice(msg string)` L60, rejection/validation, ~3s.
- `setFailure(msg string)` L69, one-shot failure, ~8s, also appended to the events ring buffer.
- `setDegraded(msg string)` L81, sticky until cleared; `clearStatus()` L93.
- `setInlineTransientStatus(msg string, d time.Duration) tea.Cmd` L29, success severity with explicit duration; returns a tick cmd that emits `statusClearMsg`.
- (`setStatusError` L52 is marked temporary; do not use.)

Use from an Update handler: call the setter on the returned model, e.g. `m.setStatus(...)`, return `m, nil`. They render in the floating bubble (`toast_bubble.go`, wraps at 40 cols). Mapping: ok/info → `setStatus`, warn/rejected → `setNotice`, error → `setFailure`. Note `handleKeyPress` clears the toast at its top (L50-52), so a toast set during a key handler stays until the next key.

## 7. Help / footer

- `viewSpecificKeyMap()` (`model_footer.go` L816) maps `m.mode` to `m.keys.ListNormal | Tree | BoardNormal | Epics | ...` (value-type maps held in `AppKeys`, `pkg/ui/keys/keys.go`). `l1KeyMap()` L789 → footer `ShortHelp()` (`footer` L766). `sidebarHelpGroups()` L891 → `km.FullHelp()` for the `;` sidebar; the `?` overlay shows Global.
- No dynamic-append hook. Cheapest way to show plugin actions: in `viewSpecificKeyMap()`, wrap the returned map in a small type implementing `help.KeyMap` that appends one extra `FullHelp()` group built from `key.NewBinding(key.WithKeys(a.Key), key.WithHelp(a.Key, a.Label))` for each action applicable to the view. Bindings are built at runtime, so none of the static tests see them. Alternative with no code in the help path: show them only in the action menu (section 5c) plus one static `v` hint.
- Static `v` binding constraints (if added to `ListNormalKeys`/`BoardNormalKeys`/`TreeKeys`/`EpicsKeys`):
  - `TestAllBindings_HaveHelpDesc` (keys_test.go L63): non-empty `WithHelp` desc, and the binding must appear in `FullHelp()`/`ShortHelp()` to be checked.
  - `TestReadmeKeyTables` (readme_keys_test.go L27): only checks keys *listed in the README* tables exist in some map (rule 1) and that single-key README rows share a word with the Global/ListNormal help text (rule 2). Adding a binding does not require a README change; if README adds `v`, the `ListNormal` help text must share a word (>2 chars) with the README description.
  - `TestUniversalNav_ConsistentAcrossViews` only covers names Up/Down/Left/Right/Enter/Esc/Back, so name the new field e.g. `PluginMenu`.
  - Epics already uses `v` (`Card`), so a static `v` there needs the conflict resolved first (section 2).
  - Keep `AppKeys` field + `allMaps()` registration in sync for any new Map.

## 8. Quitting and `--popup`

- Quit ways: `tea.Quit` returned from a handler (18 sites in `pkg/ui`, e.g. model_update_input.go L598 quit-confirm `y`, L963 `Global.Quit` ctrl+c, L1027 the `q` cascade end). `ModalQuitConfirm`/`focusQuitConfirm` is opened only from the settings menu's QUIT entry (`settings_menu_keys.go` L38-39); `q` at the root view quits immediately (L1027), not via the confirm.
- So a `dismiss` result with popup mode: `return m, tea.Quit` directly from the result handler. If a toast should be visible first, there is no delay hook: either skip the toast or use `tea.Sequence(tea.Tick(...), tea.Quit)`; in popup mode the process exit makes the toast moot.
- Terminal teardown is handled by `runTUIProgram` (`cmd/bt/root.go` L792-910: `p.Run()`, mouse-mode teardown, `defer m.Stop()` on the model at callers L455, ~L494+); quitting via `tea.Quit` is the supported exit path.
- CLI flags: `cmd/bt/root.go`. Package-level vars at L45-60 (`flagRecipe`, `flagBQL`, ...), registered in `init` at L110-125 on `f := rootCmd.Flags()` (root-only TUI flags):
  ```go
  f.BoolVar(&flagAllowHooks, "allow-hooks", false, "Bypass trust check ...")   // L124 pattern
  ```
  Add `flagPopup bool` + `f.BoolVar(&flagPopup, "popup", false, "...")`. The model is constructed at L454 (`--as-of` path) and L494 (normal path): `m := ui.NewModel(appCtx.issues, activeRecipe, appCtx.beadsPath, appCtx.selectedSource, appCtx.routeTable)`; subsequent setters follow the pattern `m.SetProjectName(...)` (`pkg/ui/model_modes.go` L126, `SetFilter` L131, `SetCurrentProjectDB` L172). Add `func (m *Model) SetPopupMode(on bool)` + a `popupMode bool` field, call `m.SetPopupMode(flagPopup)` right after `NewModel` at L494 (and L454 if popup should work with `--as-of`).
