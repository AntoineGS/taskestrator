# feat(tui): slots registry for row badges, detail sections and BQL fields

Part 1 of the plugin work proposed in #<issue>.

## What changed

Badges on rows, sections in the detail pane and BQL fields were each hard-coded where they render, in four separate row renderers and one inline detail builder. This adds `pkg/ui/slots`, a registry that built-in features and future extensions both use.

- **`pkg/ui/slots` registry.** Badge, section and field providers, safe for concurrent use. Design notes in `docs/design/tui-slots.md`.
- **Badges on every row renderer.** List rows, board cards, tree rows and epics child rows draw provider badges through one width-budgeted helper. List, tree and epics rows never squeeze the title below 20 cells; board cards use only the space left on the first line between the ID and the age. The overdue/stale badge is now a provider, so it also appears on board cards, tree rows and epics child rows.
- **Detail sections.** Providers' sections render right after the property block. Capabilities is now a provider.
- **BQL extension fields.** `ExecuteOpts.Fields` resolves fields outside `ValidFields`; the TUI's BQL modal accepts registry fields. Built-in field names cannot be shadowed.

No plugin code is included.

## Visible behaviour change

- The overdue/stale badge now also appears on board cards, tree rows and epics child rows.
- On list rows, the overdue/stale badge is now dropped when keeping it would shrink the title below 20 cells. Previously it always showed above 80 columns.

## Testing

`pkg/ui` was run with `HOME` and `XDG_CONFIG_HOME` pointed at an empty directory, because a personal theme config on my machine breaks three theme tests. The other packages were run with the real `HOME`. Under that setup the only failure is `TestMemoriesLoadCmd_DogfoodLiveProject`, which needs the maintainer's private Beads database. If you rerun the whole suite with an empty `HOME` under `/tmp`, `cmd/bt` `TestIsEphemeralAnchorPath/normal_project_path` also fails, because `/tmp` counts as an ephemeral path.

- `go build ./...` and `go vet ./...`: clean.
- `gofmt -l` on the Go files changed by this branch: prints nothing.
- `go test ./...` (setup above): all packages pass except `TestMemoriesLoadCmd_DogfoodLiveProject`.
- `go test -race ./pkg/ui/slots/ ./pkg/bql/ ./pkg/ui/`: `pkg/ui/slots` and `pkg/bql` pass; `pkg/ui` fails only on the test above.
- `go test -covermode=atomic ./pkg/ui/slots/ ./pkg/bql/`: `pkg/ui/slots` 100.0%, `pkg/bql` 77.4%.
