# bt plugin host, bt plugin actions, and `tsk plugin` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tsk's features usable inside bt: live agent badges, an agent detail section, `tsk.state` BQL filtering, and `D` dispatch / `J` jump on the selected bead, with tsk running as a bt plugin.

**Architecture:** bt gains `pkg/plugin`, a host that starts each configured plugin executable, speaks JSON-RPC 2.0 over newline-delimited JSON on its stdio, sends it bead snapshots and turns its per-bead state into slot badges, detail sections and BQL fields through the existing `pkg/ui/slots` registry. `pkg/ui` adds plugin action keys, an action menu and confirm/select prompts. tsk gains `internal/btplugin`, which answers that protocol: it maps synced beads plus OpenCode session snapshots to `tsk.state`, follows the OpenCode event stream, and runs the existing dispatch and jump code as actions. tsk's own TUI is removed at the end.

**Tech Stack:** Go; bt: Bubble Tea v2, yaml.v3; tsk: Go 1.27, BurntSushi/toml.

**Spec:** `docs/superpowers/specs/2026-10-07-bt-plugins-design.md` (Part 1 "Protocol", "Slots", "Keys and the action menu", `--popup`; Part 2 all; "First local version" overrides). Integration notes gathered for this plan: `docs/superpowers/notes/bt-host-integration.md`, `docs/superpowers/notes/bt-actions-integration.md`, `docs/superpowers/notes/tsk-plugin-integration.md`.

## Global Constraints

Both repos:
- Protocol version 1. JSON-RPC 2.0, one JSON object per line, UTF-8, lines ≤ 4 MiB. Requests carry `id`; notifications do not. Either side may send requests. Plugin errors use code `-32000` with `data.type`.
- Method names exactly: `initialize`, `beads.sync`, `state.set`, `state.clear`, `action.invoke`, `ui.confirm`, `ui.select`, `ui.toast`, `shutdown`.
- Timeouts: `initialize` 5s, `action.invoke` 60s. Restart backoff 1s, 5s, 25s; failed after the third failure. Shutdown: notify `shutdown`, close stdin, wait 2s, kill. Redraw coalescing 100ms. Malformed lines: drop and log; kill the plugin after 10.
- Tones: `accent`, `ok`, `warn`, `error`, `muted`. Badges ≤ 6 cells. IDs match `^[a-z][a-z0-9_]*$`. Sections ≤ 16 KiB markdown.
- tsk manifest values: name `tsk`; field `state` label `Agent` with values `waiting` (WAIT, warn), `running` (RUN, accent), `review` (REV, ok), `lost` (LOST, error), `unknown` (`?`, muted, lane false), `queued` (no badge, lane false); section `agent` titled `Agent`; actions `dispatch` "Dispatch to agent" key `D`, `jump` "Jump to agent tab" key `J`; subscribe `metadata_prefixes ["tsk."]`, `statuses ["open","in_progress","review","blocked"]`.
- Never log or send the OpenCode service password. Never push, fork, open issues/PRs, or `go install`/`make install` without the user's approval.

bt (`/home/a.simard@multidev.local/gits/beadstui/.claude/worktrees/plugin-slots`, new branch `feat/plugin-host` stacked on `feat/plugin-slots`):
- Follow `beadstui/AGENTS.md`: no file deletion, no destructive git, no script-based edits, `go build ./...` and `go vet ./...` after changes, `fmt.Errorf("…: %w", err)`, no raw prints in production code (use `pkg/debug`).
- Commits `type(scope): description`; scopes `tui`, `cli`, `data`, `docs`, `tests`.
- Nothing in bt names tsk, OpenCode or herdr (test fixtures use a plugin named `example`).
- pkg/ui tests run with `HOME=/tmp/bt-test-home XDG_CONFIG_HOME=/tmp/bt-test-home/.config`; `TestMemoriesLoadCmd_DogfoodLiveProject` is a known environmental failure. gofmt is checked only on files the branch changes.
- Plugins never run in `bt robot` or `--as-of` mode.

tsk (`/home/a.simard@multidev.local/gits/taskestrator/.worktrees/bt-plugin`, branch `feat/bt-plugin` from `main`):
- `make build`, `make test`, `make lint` (vet + gofmt) clean after each task. Commits in the repo's style (`feat: …`, `fix: …`, `refactor: …`, `docs: …`, `test: …`).
- `internal/btplugin` is written independently of bt's `pkg/plugin` (do not copy bt source into tsk).
- Keep the typed-nil-interface rule from `cmd/tsk/main.go`: assign interface-typed deps only on success.

## Review Focus

1. A plugin that hangs (never answers `initialize`, or blocks in `action.invoke`) must never freeze bt's UI; bt keeps rendering and the action times out as "result unknown". (Task 3, 5 tests.)
2. A plugin that crashes mid-action must not be replayed; the action reports "result unknown" and state from the dead process disappears. (Task 3 test.)
3. Thousands of beads in global mode: `beads.sync` must not be sent when data did not change, and badge lookups on the render path must not do file IO or block on plugin I/O. (Tasks 3, 4.)
4. Keys typed into filters, searches, the BQL modal or other modals must never trigger plugin actions; plugin keys never shadow bt's own bindings. (Task 5 test.)
5. tsk losing the OpenCode service mid-run: beads go `unknown` with one toast, and recover to real states after the service returns without restarting bt. (Task 11 test.)

---

## Part A — bt

### Task 0: Branch

- [ ] **Step 1**

```bash
cd /home/a.simard@multidev.local/gits/beadstui/.claude/worktrees/plugin-slots
git status --short   # only the untracked .beads.gate.lock is allowed
git checkout -b feat/plugin-host
```

---

### Task 1: Protocol types and manifest validation

**Files:**
- Create: `pkg/plugin/protocol.go`, `pkg/plugin/protocol_test.go`

**Interfaces — Produces:**
- `const ProtocolVersion = 1`
- Types `Scope`, `InitializeParams`, `Manifest`, `Subscribe`, `Field`, `FieldValue`, `SectionDecl`, `ActionDecl`, `Bead`, `BeadKey`, `BeadRef`, `SyncParams`, `StateSetParams`, `BeadState`, `FieldState`, `StateClearParams`, `InvokeParams`, `InvokeResult`, `Toast`, `ConfirmParams`, `SelectParams`, `SelectOption` (exact JSON tags below)
- `func (m Manifest) Validate(configName string) error`
- `func (v FieldValue) InLane() bool`

- [ ] **Step 1: Write the failing tests** — `pkg/plugin/protocol_test.go`:

```go
package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		ProtocolVersion: ProtocolVersion,
		Name:            "example",
		Version:         "0.1.0",
		Fields: []Field{{ID: "state", Label: "Agent", Values: []FieldValue{
			{ID: "waiting", Label: "Waiting", Badge: "WAIT", Tone: "warn"},
			{ID: "queued", Label: "Queued"},
		}}},
		Sections: []SectionDecl{{ID: "agent", Title: "Agent"}},
		Actions:  []ActionDecl{{ID: "dispatch", Label: "Dispatch", Key: "D"}},
	}
}

func TestManifestValidate(t *testing.T) {
	if err := validManifest().Validate("example"); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	cases := map[string]func(m *Manifest){
		"protocol version": func(m *Manifest) { m.ProtocolVersion = 2 },
		"name mismatch":    func(m *Manifest) { m.Name = "other" },
		"bad field id":     func(m *Manifest) { m.Fields[0].ID = "State" },
		"dup value id":     func(m *Manifest) { m.Fields[0].Values[1].ID = "waiting" },
		"bad tone":         func(m *Manifest) { m.Fields[0].Values[0].Tone = "red" },
		"wide badge":       func(m *Manifest) { m.Fields[0].Values[0].Badge = "WAITING" },
		"bad action id":    func(m *Manifest) { m.Actions[0].ID = "do-it" },
		"dup section":      func(m *Manifest) { m.Sections = append(m.Sections, SectionDecl{ID: "agent", Title: "x"}) },
		"empty key":        func(m *Manifest) { m.Actions[0].Key = "" },
	}
	for name, mutate := range cases {
		m := validManifest()
		m.Fields[0].Values = append([]FieldValue(nil), m.Fields[0].Values...)
		mutate(&m)
		if err := m.Validate("example"); err == nil {
			t.Errorf("%s: invalid manifest accepted", name)
		}
	}
}

func TestFieldValueInLane(t *testing.T) {
	no := false
	if !(FieldValue{}).InLane() || (FieldValue{Lane: &no}).InLane() {
		t.Fatal("InLane: default should be true, explicit false false")
	}
}

func TestBeadRepoNullEncoding(t *testing.T) {
	b, _ := json.Marshal(BeadRef{DB: "proj", ID: "proj-1"})
	if !strings.Contains(string(b), `"repo":null`) {
		t.Fatalf("missing repo must encode as null: %s", b)
	}
	repo := "/r"
	b, _ = json.Marshal(BeadRef{DB: "proj", ID: "proj-1", Repo: &repo})
	if !strings.Contains(string(b), `"repo":"/r"`) {
		t.Fatalf("repo not encoded: %s", b)
	}
}
```

- [ ] **Step 2: Run** `go test ./pkg/plugin/` — expect FAIL (`undefined: Manifest`).

- [ ] **Step 3: Implement** — `pkg/plugin/protocol.go`:

```go
// Package plugin hosts external plugin processes. A plugin is an executable
// that speaks JSON-RPC 2.0 over newline-delimited JSON on its stdin and
// stdout. bt sends it bead snapshots and action requests; the plugin sends
// back per-bead state that bt renders as badges, detail sections and BQL
// fields. See docs/plugins.md for the protocol.
package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"charm.land/lipgloss/v2"
)

// ProtocolVersion is the protocol major version this bt speaks.
const ProtocolVersion = 1

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var validTones = map[string]bool{"": true, "accent": true, "ok": true, "warn": true, "error": true, "muted": true}

// maxBadgeWidth is the widest badge a plugin may declare, in cells.
const maxBadgeWidth = 6

// Scope tells a plugin which beads bt is showing.
type Scope struct {
	Mode      string   `json:"mode"` // project, global or workspace
	Databases []string `json:"databases,omitempty"`
}

// InitializeParams are sent with the initialize request.
type InitializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
	BT              struct {
		Version string `json:"version"`
	} `json:"bt"`
	Scope   Scope          `json:"scope"`
	Popup   bool           `json:"popup"`
	Options map[string]any `json:"options"`
}

// Manifest is a plugin's answer to initialize.
type Manifest struct {
	ProtocolVersion int           `json:"protocolVersion"`
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Subscribe       Subscribe     `json:"subscribe"`
	Fields          []Field       `json:"fields"`
	Sections        []SectionDecl `json:"sections"`
	Actions         []ActionDecl  `json:"actions"`
}

// Subscribe narrows beads.sync. Empty Statuses means every status; empty
// MetadataPrefixes means no metadata is sent.
type Subscribe struct {
	MetadataPrefixes []string `json:"metadata_prefixes"`
	Statuses         []string `json:"statuses"`
}

// Field is an enum-valued per-bead field a plugin publishes.
type Field struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Values []FieldValue `json:"values"`
}

// FieldValue is one value of a Field. A value without Badge draws no badge.
type FieldValue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Badge string `json:"badge,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Lane  *bool  `json:"lane,omitempty"`
}

// InLane reports whether the value gets a board lane (default true).
func (v FieldValue) InLane() bool { return v.Lane == nil || *v.Lane }

// SectionDecl declares a detail-pane section.
type SectionDecl struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ActionDecl declares an action on the selected bead.
type ActionDecl struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Key   string `json:"key"`
}

// BeadKey identifies a bead across databases.
type BeadKey struct {
	DB string `json:"db"`
	ID string `json:"id"`
}

// BeadRef is a BeadKey plus the checkout path, null when unknown.
type BeadRef struct {
	DB   string  `json:"db"`
	ID   string  `json:"id"`
	Repo *string `json:"repo"`
}

// Bead is one entry of beads.sync.
type Bead struct {
	DB       string                     `json:"db"`
	ID       string                     `json:"id"`
	Repo     *string                    `json:"repo"`
	Status   string                     `json:"status"`
	Title    string                     `json:"title"`
	Type     string                     `json:"type"`
	Priority int                        `json:"priority"`
	Assignee string                     `json:"assignee"`
	Metadata map[string]json.RawMessage `json:"metadata"`
}

// SyncParams are the params of beads.sync.
type SyncParams struct {
	Revision int    `json:"revision"`
	Beads    []Bead `json:"beads"`
}

// FieldState is a bead's value for one field.
type FieldState struct {
	Value string     `json:"value"`
	Since *time.Time `json:"since,omitempty"`
}

// BeadState is a plugin's full state for one bead.
type BeadState struct {
	DB       string                `json:"db"`
	ID       string                `json:"id"`
	Fields   map[string]FieldState `json:"fields,omitempty"`
	Sections map[string]string     `json:"sections,omitempty"`
	Actions  []string              `json:"actions,omitempty"`
}

// StateSetParams are the params of state.set.
type StateSetParams struct {
	Replace bool        `json:"replace"`
	Beads   []BeadState `json:"beads"`
}

// StateClearParams are the params of state.clear.
type StateClearParams struct {
	Beads []BeadKey `json:"beads"`
}

// InvokeParams are the params of action.invoke.
type InvokeParams struct {
	Action string  `json:"action"`
	Bead   BeadRef `json:"bead"`
	View   string  `json:"view"`
}

// Toast is a short message shown in bt's footer.
type Toast struct {
	Message string `json:"message"`
	Tone    string `json:"tone,omitempty"`
}

// InvokeResult is the result of action.invoke.
type InvokeResult struct {
	Toast   *Toast `json:"toast,omitempty"`
	Dismiss bool   `json:"dismiss,omitempty"`
}

// ConfirmParams are the params of ui.confirm.
type ConfirmParams struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Confirm string `json:"confirm,omitempty"`
	Cancel  string `json:"cancel,omitempty"`
}

// SelectOption is one choice of ui.select.
type SelectOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SelectParams are the params of ui.select.
type SelectParams struct {
	Title   string         `json:"title"`
	Options []SelectOption `json:"options"`
}

// Validate checks a manifest against the configured plugin name.
func (m Manifest) Validate(configName string) error {
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("protocol version %d, bt speaks %d", m.ProtocolVersion, ProtocolVersion)
	}
	if m.Name != configName {
		return fmt.Errorf("manifest name %q does not match config name %q", m.Name, configName)
	}
	seen := map[string]bool{}
	for _, f := range m.Fields {
		if err := checkID("field", f.ID, seen); err != nil {
			return err
		}
		values := map[string]bool{}
		for _, v := range f.Values {
			if err := checkID("value of field "+f.ID, v.ID, values); err != nil {
				return err
			}
			if !validTones[v.Tone] {
				return fmt.Errorf("field %s value %s: unknown tone %q", f.ID, v.ID, v.Tone)
			}
			if lipgloss.Width(v.Badge) > maxBadgeWidth {
				return fmt.Errorf("field %s value %s: badge %q wider than %d cells", f.ID, v.ID, v.Badge, maxBadgeWidth)
			}
		}
	}
	sections := map[string]bool{}
	for _, s := range m.Sections {
		if err := checkID("section", s.ID, sections); err != nil {
			return err
		}
	}
	actions := map[string]bool{}
	for _, a := range m.Actions {
		if err := checkID("action", a.ID, actions); err != nil {
			return err
		}
		if a.Key == "" {
			return fmt.Errorf("action %s: empty key", a.ID)
		}
	}
	return nil
}

func checkID(kind, id string, seen map[string]bool) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%s id %q must match %s", kind, id, idPattern)
	}
	if seen[id] {
		return fmt.Errorf("duplicate %s id %q", kind, id)
	}
	seen[id] = true
	return nil
}
```

- [ ] **Step 4: Run** `go test ./pkg/plugin/ -v` — PASS.
- [ ] **Step 5: Commit** `feat(tui): plugin protocol types and manifest validation`

---

### Task 2: JSON-RPC connection

**Files:**
- Create: `pkg/plugin/rpc.go`, `pkg/plugin/rpc_test.go`

**Interfaces — Produces:**
- `type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)`
- `type RPCError struct { Code int; Message string; Data json.RawMessage }` implementing `error`; `const CodePluginError = -32000`; `func PluginError(typ, message string) *RPCError`
- `func NewConn(r io.Reader, w io.Writer, h Handler, onBadLine func(error)) *Conn`
- `(*Conn).Run(ctx) error` (blocks reading until EOF/ctx; afterwards all pending calls fail with `ErrClosed`)
- `(*Conn).Call(ctx, method string, params, result any) error`
- `(*Conn).Notify(method string, params any) error`
- `var ErrClosed = errors.New("plugin connection closed")`
- Semantics: requests from the peer run the handler in their own goroutine and reply; notifications run the handler synchronously in arrival order (ordering of `state.set` matters); unknown methods get `-32601` for requests and are ignored for notifications; lines > 4 MiB or invalid JSON call `onBadLine`.

- [ ] **Step 1: Write the failing tests** — `pkg/plugin/rpc_test.go`:

```go
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// pipePair returns two connected Conns.
func pipePair(t *testing.T, ha, hb Handler) (*Conn, *Conn) {
	t.Helper()
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	a := NewConn(ar, aw, ha, nil)
	b := NewConn(br, bw, hb, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); aw.Close(); bw.Close() })
	go a.Run(ctx)
	go b.Run(ctx)
	return a, b
}

func TestCallRoundTrip(t *testing.T) {
	echo := func(_ context.Context, method string, p json.RawMessage) (any, error) {
		if method != "echo" {
			return nil, &RPCError{Code: -32601, Message: "no"}
		}
		var v map[string]string
		_ = json.Unmarshal(p, &v)
		return v, nil
	}
	a, _ := pipePair(t, nil, echo)
	var out map[string]string
	if err := a.Call(context.Background(), "echo", map[string]string{"x": "1"}, &out); err != nil || out["x"] != "1" {
		t.Fatalf("Call = %v, %v", out, err)
	}
}

func TestCallPluginError(t *testing.T) {
	fail := func(context.Context, string, json.RawMessage) (any, error) {
		return nil, PluginError("no_repo", "no checkout")
	}
	a, _ := pipePair(t, nil, fail)
	err := a.Call(context.Background(), "x", nil, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodePluginError || rpcErr.Message != "no checkout" {
		t.Fatalf("err = %v", err)
	}
	if rpcErr.Type() != "no_repo" {
		t.Fatalf("Type() = %q", rpcErr.Type())
	}
}

func TestNotificationsKeepOrder(t *testing.T) {
	var mu sync.Mutex
	var got []int
	done := make(chan struct{})
	h := func(_ context.Context, _ string, p json.RawMessage) (any, error) {
		var n int
		_ = json.Unmarshal(p, &n)
		mu.Lock()
		got = append(got, n)
		if len(got) == 50 {
			close(done)
		}
		mu.Unlock()
		return nil, nil
	}
	a, _ := pipePair(t, nil, h)
	for i := 0; i < 50; i++ {
		if err := a.Notify("n", i); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notifications not delivered")
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("order broken at %d: %v", i, got)
		}
	}
}

func TestRequestHandlerDoesNotBlockReader(t *testing.T) {
	release := make(chan struct{})
	h := func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "slow" {
			<-release
		}
		return "ok", nil
	}
	a, _ := pipePair(t, nil, h)
	go a.Call(context.Background(), "slow", nil, nil)
	var out string
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Call(ctx, "fast", nil, &out); err != nil || out != "ok" {
		t.Fatalf("fast call blocked behind slow one: %v", err)
	}
	close(release)
}

func TestCallTimeoutAndClose(t *testing.T) {
	block := make(chan struct{})
	h := func(context.Context, string, json.RawMessage) (any, error) { <-block; return nil, nil }
	a, b := pipePair(t, nil, h)
	_ = b
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.Call(ctx, "x", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	close(block)
}

func TestBadLinesReported(t *testing.T) {
	r, w := io.Pipe()
	var mu sync.Mutex
	bad := 0
	c := NewConn(r, io.Discard, nil, func(error) { mu.Lock(); bad++; mu.Unlock() })
	done := make(chan error)
	go func() { done <- c.Run(context.Background()) }()
	_, _ = w.Write([]byte("not json\n{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n{]\n"))
	w.Close()
	<-done
	if bad != 2 {
		t.Fatalf("bad lines = %d, want 2", bad)
	}
	if err := c.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Call after close = %v, want ErrClosed", err)
	}
}
```

- [ ] **Step 2: Run** — FAIL (`undefined: NewConn`).

- [ ] **Step 3: Implement** — `pkg/plugin/rpc.go`:

```go
package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// maxLine is the longest JSON-RPC message accepted, in bytes.
const maxLine = 4 << 20

// CodePluginError is the JSON-RPC error code for plugin-defined failures.
const CodePluginError = -32000

// ErrClosed is returned by calls on a connection whose peer went away.
var ErrClosed = errors.New("plugin connection closed")

// Handler answers a request or handles a notification from the peer.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// Type returns data.type of a plugin error, or "".
func (e *RPCError) Type() string {
	var d struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(e.Data, &d)
	return d.Type
}

// PluginError builds a -32000 error with data.type set.
func PluginError(typ, message string) *RPCError {
	data, _ := json.Marshal(map[string]string{"type": typ})
	return &RPCError{Code: CodePluginError, Message: message, Data: data}
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type reply struct {
	result json.RawMessage
	err    error
}

// Conn is one JSON-RPC 2.0 connection over newline-delimited JSON.
type Conn struct {
	r         io.Reader
	h         Handler
	onBadLine func(error)

	wmu sync.Mutex
	w   io.Writer

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan reply
	closed  bool
}

// NewConn returns a connection reading r and writing w. h handles the peer's
// requests and notifications and may be nil. onBadLine, when set, is called
// for every line that is not a valid message.
func NewConn(r io.Reader, w io.Writer, h Handler, onBadLine func(error)) *Conn {
	return &Conn{r: r, w: w, h: h, onBadLine: onBadLine, pending: map[string]chan reply{}}
}

// Run reads messages until r ends or ctx is done. Afterwards every pending
// and future call fails with ErrClosed.
func (c *Conn) Run(ctx context.Context) error {
	defer c.close()
	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
			if err == nil {
				err = errors.New("missing jsonrpc 2.0")
			}
			c.bad(fmt.Errorf("invalid message: %w", err))
			continue
		}
		c.dispatch(ctx, m)
	}
	if err := sc.Err(); err != nil {
		c.bad(fmt.Errorf("read: %w", err))
		return err
	}
	return nil
}

func (c *Conn) bad(err error) {
	if c.onBadLine != nil {
		c.onBadLine(err)
	}
}

func (c *Conn) dispatch(ctx context.Context, m message) {
	switch {
	case m.Method == "" && len(m.ID) > 0: // response
		c.mu.Lock()
		ch, ok := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if !ok {
			c.bad(fmt.Errorf("response to unknown id %s", m.ID))
			return
		}
		if m.Error != nil {
			ch <- reply{err: m.Error}
		} else {
			ch <- reply{result: m.Result}
		}
	case m.Method != "" && len(m.ID) > 0: // request
		go func() {
			result, err := c.handle(ctx, m.Method, m.Params)
			resp := message{JSONRPC: "2.0", ID: m.ID}
			if err != nil {
				var rpcErr *RPCError
				if !errors.As(err, &rpcErr) {
					rpcErr = &RPCError{Code: -32603, Message: err.Error()}
				}
				resp.Error = rpcErr
			} else {
				b, mErr := json.Marshal(result)
				if mErr != nil {
					resp.Error = &RPCError{Code: -32603, Message: mErr.Error()}
				} else {
					resp.Result = b
				}
			}
			_ = c.write(resp)
		}()
	case m.Method != "": // notification
		_, _ = c.handle(ctx, m.Method, m.Params)
	default:
		c.bad(errors.New("message is neither request, response nor notification"))
	}
}

func (c *Conn) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if c.h == nil {
		return nil, &RPCError{Code: -32601, Message: "method not found: " + method}
	}
	return c.h(ctx, method, params)
}

func (c *Conn) write(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(message{JSONRPC: "2.0", Method: method, Params: p})
}

// Call sends a request and waits for its response, ctx, or the connection
// closing. result may be nil.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := json.RawMessage(strconv.FormatInt(c.nextID, 10))
	ch := make(chan reply, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()

	if err := c.write(message{JSONRPC: "2.0", ID: id, Method: method, Params: p}); err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, result)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Conn) forget(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, string(id))
	c.mu.Unlock()
}

func (c *Conn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- reply{err: ErrClosed}
		delete(c.pending, id)
	}
}
```

- [ ] **Step 4: Run** `go test -race ./pkg/plugin/ -v` — PASS.
- [ ] **Step 5: Commit** `feat(tui): JSON-RPC connection for plugins`

---

### Task 3: Config and host (processes, handshake, sync, state, actions, prompts)

**Files:**
- Create: `pkg/plugin/config.go`, `pkg/plugin/config_test.go`, `pkg/plugin/host.go`, `pkg/plugin/state.go`, `pkg/plugin/host_test.go`, `pkg/plugin/testplugin_test.go`

**Interfaces — Consumes:** Task 1 types, Task 2 `Conn`; `pkg/ui/slots` (`Registry.AddBadges/AddSections/AddFields`, `Badge`, `Section`, `Context`, `Tone*`); `pkg/model.Issue`.

**Interfaces — Produces:**

```go
// config.go
type Config struct {
	Name    string            `yaml:"name"`
	Command []string          `yaml:"command"`
	Enabled *bool             `yaml:"enabled"`
	Env     map[string]string `yaml:"env"`
	Options map[string]any    `yaml:"options"`
	Keys    map[string]string `yaml:"keys"`
}
func (c Config) IsEnabled() bool                 // nil → true
func DefaultConfigPath() (string, error)         // $HOME/.config/bt/config.yaml (same path root.go reads)
func LoadConfig(path string) ([]Config, error)   // missing file → nil, nil; validates name pattern, non-empty command; duplicate names: later entry disabled with error returned alongside? → return the list with the duplicate removed and a joined error (errors.Join) describing it

// host.go
type Options struct {
	Configs   []Config
	BTVersion string
	Scope     Scope
	Popup     bool
	DB        func(issue *model.Issue) string // cheap; called on the render path
	Repo      func(issue *model.Issue) string // may do IO; "" when unknown; called only when syncing or invoking
}
type Status struct { Name, State, Version, LastError string; Restarts int } // State: disabled|starting|active|failed
type Action struct { Plugin, ID, Label, Key string }
type ActionResult struct { Toast *Toast; Dismiss bool; Unknown bool } // Unknown: timeout/crash, never replayed

func NewHost(opts Options) *Host
func (h *Host) SetSender(send func(tea.Msg))
func (h *Host) Start()                         // starts every enabled plugin
func (h *Host) Stop()                          // shutdown, 2s grace, kill; idempotent
func (h *Host) Register(reg *slots.Registry)   // badge, section and field providers
func (h *Host) SyncIssues(issues []model.Issue) // converts synchronously (copies), sends async to active plugins
func (h *Host) Actions(issue *model.Issue) []Action // from pushed state, config order; Key after config override ("none" omits key, action still in menu)
func (h *Host) Invoke(ctx context.Context, a Action, issue *model.Issue, view string) ActionResult // blocks ≤60s
func (h *Host) Statuses() []Status
func (h *Host) FieldPrefixes() []string        // "<plugin>." for active plugins (to decide BQL reapply)

// Messages delivered through the sender (exported for pkg/ui):
type StateChangedMsg struct{ Beads []BeadKey }             // coalesced every 100ms
type ToastMsg struct{ Plugin string; Toast Toast }
type StatusMsg struct{ Status Status }                      // on transitions to active/failed
type PromptMsg struct {
	Plugin  string
	Confirm *ConfirmParams
	Select  *SelectParams
	Reply   func(answer any) // call exactly once; nil answer = dismissed
}
```

Behaviour (all from the spec's Part 1 Lifecycle/Transport/Protocol and Global Constraints):
- One supervisor goroutine per enabled plugin: `exec.CommandContext(ctx, cmd[0], cmd[1:]...)`, `Env = os.Environ() + cfg.Env`, `Dir` = bt's cwd, stdin/stdout pipes, stderr drained line by line into a 50-line ring (`Status.LastError` is the last stderr line or the failure reason) and to `debug.Log` when `debug.Enabled()`.
- Handshake: `initialize` with `InitializeParams{ProtocolVersion, BT.Version, Scope, Popup, Options: cfg.Options}`, 5s timeout; `Manifest.Validate(cfg.Name)`; failure → kill, count as a failure.
- When active: send `beads.sync` with the latest beads filtered by `Manifest.Subscribe` (statuses; metadata keys whose name starts with a subscribed prefix; no prefixes → no metadata), `Revision` incremented per send. Later `SyncIssues` calls send only when the filtered payload differs from the last one sent to that plugin (compare a sha256 of the encoded payload).
- Incoming notifications: `state.set` (validate field/value IDs against the manifest; drop unknown fields with `debug.Log`; truncate sections > 16 KiB; `Replace` drops the plugin's previous entries first), `state.clear`, `ui.toast` (rate limit: at most one per plugin per second, extra dropped) → `ToastMsg`. Any other method → ignored (notification) or `-32601` (request).
- Incoming requests `ui.confirm` / `ui.select`: allowed only while an `Invoke` for the same plugin is in flight; otherwise `PluginError("no_action", …)`. Send a `PromptMsg` whose `Reply` delivers the answer to the waiting handler; if the action ends first, the handler returns `PluginError("cancelled", …)`.
- State store: `map[pluginName]map[BeadKey]BeadState` behind a `sync.RWMutex`. A dirty set of BeadKeys; a 100ms ticker goroutine sends one `StateChangedMsg{Beads}` when dirty.
- Crash/exit while active or starting: drop all state of that plugin (mark its keys dirty), every pending `Call` returns `ErrClosed` (→ `ActionResult{Unknown: true}`), restart after 1s, 5s, 25s; after the third failure State=`failed` and a `StatusMsg`. 10 bad lines → kill (counts as a crash).
- Providers (registered by `Register`):
  - Badges: for each active plugin in config order, its state for `BeadKey{DB(issue), issue.ID}`, each manifest field in order whose current value has a `Badge` → `slots.Badge{Text, Tone, Since}` (tone strings map to `slots.Tone*`, "" → muted).
  - Sections: manifest section order, non-empty markdown → `slots.Section{Title, Markdown}`.
  - Fields: `"<plugin>.<field>"` for active plugins; value = current value id.
  - Render-path reads take only the read lock; never call `Repo` or do IO.
- `Invoke`: resolve `Repo(issue)` (empty → `Repo: nil`), `Call(ctx 60s, "action.invoke", InvokeParams{…})`; errors: `*RPCError` → `ActionResult{Toast: &Toast{Message: err.Message, Tone: "error"}}`; timeout/`ErrClosed` → `Unknown: true`.
- `Stop`: cancel supervisors; for each live process `Notify("shutdown")`, close stdin, wait up to 2s, then kill; stop the ticker.

Test plugin (`testplugin_test.go`): the standard Go helper-process pattern. `TestMain` checks `os.Getenv("BT_TEST_PLUGIN")`; when set, it runs a scripted plugin on stdin/stdout instead of the tests and exits. Behaviours selected by the env value, comma-separated:
- `ok`: answers `initialize` with the `validManifest()` manifest named from env `BT_TEST_PLUGIN_NAME` (default `example`); on `beads.sync` replies with `state.set` giving every synced bead `state=waiting`, section `agent` = `"sync rev N"`, actions `["dispatch"]`; on `action.invoke` returns `{toast:{message:"did <action> <id>"}}`.
- `hang-init`: never answers `initialize`.
- `bad-manifest`: answers with protocol version 2.
- `crash-on-invoke`: exits(3) on `action.invoke`.
- `slow-invoke`: sleeps 2 minutes on `action.invoke`.
- `confirm`: on `action.invoke` first calls `ui.confirm` and returns toast `"confirmed=<answer>"`.
- `toast-spam`: sends 20 `ui.toast` at once after initialize.
- `garbage`: writes 11 non-JSON lines after initialize.
The tests launch it with `Command: []string{os.Args[0]}` and `Env: {"BT_TEST_PLUGIN": "<behaviour>"}`. Make the timeouts and backoff package variables (`initTimeout`, `invokeTimeout`, `restartBackoff = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}`, `shutdownGrace`, `flushInterval = 100 * time.Millisecond`) so tests shorten them.

- [ ] **Step 1: Config tests and implementation** (`config_test.go`: missing file → nil; parses two plugins with options/keys/env; `enabled: false`; invalid name `Bad-Name` → error; empty command → error; duplicate name → second dropped + error). Run, see fail, implement `config.go`, see pass. Commit `feat(cli): plugin config in config.yaml`.

- [ ] **Step 2: Host tests** (`host_test.go`), each with a recording sender (`func(m tea.Msg)` appending to a mutex-guarded slice) and shortened package timers (`initTimeout=500ms`, `invokeTimeout=1s`, `restartBackoff={50ms,50ms,50ms}`, `shutdownGrace=200ms`):
  1. `TestHostHandshakeSyncAndState`: `ok` plugin (its manifest is `validManifest()`, which has an empty `Subscribe`, so every bead syncs); `SyncIssues` with two open issues `example-1` and `example-2`; wait for a `StateChangedMsg`; registry badges for `example-1` = `[WAIT]`, sections = `["sync rev 1"]`, `FieldValue("example.state")` = `waiting`; `Actions(issue)` = `[{example dispatch Dispatch D}]`.
  1b. `TestHostSubscribeFilters`: behaviour `ok-subscribed` (add to the helper: same as `ok` but `Subscribe{Statuses: ["open"], MetadataPrefixes: ["example."]}`); sync one open bead with metadata `{"example.session":"s","other":"x"}` and one closed bead; the helper echoes what it received in the section text (`"n=<count> meta=<sorted keys>"`) → `n=1 meta=example.session`.
  2. `TestHostSyncSkipsUnchangedPayload`: two identical `SyncIssues` → the plugin sees one `beads.sync` (assert via section text still `rev 1`).
  3. `TestHostInvoke`: `Invoke` returns toast `did dispatch example-1`.
  4. `TestHostKeyOverride`: config `Keys{"dispatch":"X"}` → action key `X`; `"none"` → key "".
  5. `TestHostHangingInitFails`: `hang-init` → after 3 attempts `Statuses()[0].State == "failed"`, a `StatusMsg` with failed, no panic, `Invoke` on it returns `Unknown`.
  6. `TestHostBadManifest`: status failed with LastError mentioning `protocol version`.
  7. `TestHostCrashDuringInvoke`: `crash-on-invoke` → `Invoke` returns `Unknown: true` within 1s; state for that plugin is gone (badges empty) after the next `StateChangedMsg`; plugin restarts (Restarts == 1, state active again).
  8. `TestHostSlowInvokeTimesOut`: `slow-invoke` → `Unknown: true` after ~1s; the UI goroutine was never blocked (call `Badges` concurrently during the invoke and assert it returns immediately).
  9. `TestHostConfirmPrompt`: `confirm` → a `PromptMsg` arrives; call `Reply(true)`; `Invoke` result toast `confirmed=true`.
  10. `TestHostPromptOutsideActionRejected`: drive a `ui.confirm` request with no action in flight (behaviour `confirm-unsolicited`, add it to the helper) → the helper receives error type `no_action` and reports it via `ui.toast` → assert a `ToastMsg` containing `no_action`.
  11. `TestHostToastRateLimit`: `toast-spam` → exactly 1 `ToastMsg` within 500ms.
  12. `TestHostGarbageKills`: `garbage` → plugin restarted (Restarts ≥ 1).
  13. `TestHostStopIsBounded`: `ok` plugin; `Stop()` returns within 2.5s; second `Stop()` is a no-op.
  Run with `go test -race ./pkg/plugin/`; see them fail.
- [ ] **Step 3: Implement** `state.go` (store + providers + dirty/flush) and `host.go` (supervisor, handshake, sync, handlers, invoke, prompts, stop) to the behaviour above. Keep `host.go` focused on processes/RPC and `state.go` on the store/providers; if either passes ~450 lines, split prompts into `prompt.go`.
- [ ] **Step 4: Run** `go test -race -count=3 ./pkg/plugin/` — PASS, no races, no flakes.
- [ ] **Step 5: Commit** `feat(tui): plugin host with supervision, sync, state and actions`

---

### Task 4: Wire the host into bt (sync, state redraws, toasts, `--popup`)

**Files:**
- Modify: `pkg/ui/model.go` (fields, `Init`, `Update` cases), `pkg/ui/model_update_data.go` (end of `handleSnapshotReady` and `handleDataSourceReload`), `pkg/ui/model_modes.go` (setters)
- Create: `pkg/ui/plugin_host.go`, `pkg/ui/plugin_host_test.go`
- Modify: `cmd/bt/root.go` (flag `--popup`, host construction in the normal TUI branch, `runTUIProgram`)

**Interfaces — Consumes:** `plugin.Host`, `plugin.StateChangedMsg`, `ToastMsg`, `StatusMsg`, `PromptMsg` (Task 3), `analysis.ComputeDataHash`.

**Interfaces — Produces:**
- `func (m *Model) SetPluginHost(h *plugin.Host)` — stores it and calls `h.Register(m.slotRegistry)`
- `func (m Model) PluginHost() *plugin.Host`
- `func (m *Model) SetPopupMode(on bool)`; field `popupMode bool`
- `func (m *Model) syncPlugins()` — `hash := analysis.ComputeDataHash(m.data.issues)`; if `hash != m.pluginSyncHash` → `m.pluginHost.SyncIssues(m.data.issues)`, store hash. No-op without a host.
- `type pluginSyncMsg struct{}` returned by `Init` (batched with the existing Init cmds) when a host is set; its handler calls `syncPlugins()`.

Steps:
- [ ] **Step 1: Tests** (`pkg/ui/plugin_host_test.go`, package `ui`). The Task 3 helper plugin lives in `pkg/plugin`'s test binary and cannot be launched from `pkg/ui` tests, so test the Model side with `plugin.NewHost(plugin.Options{})` (no plugins) and providers registered directly on `m.slotRegistry`:
  1. `TestSyncPluginsOnlyOnHashChange`: after `SetPluginHost`, `syncPlugins()` sets `m.pluginSyncHash`; a second call with the same issues leaves it unchanged; after `replaceIssues` with one issue's `UpdatedAt` changed, the hash changes.
  2. `TestStateChangedRefreshesDetail`: register a section provider in `m.slotRegistry` whose text changes; send `plugin.StateChangedMsg{}` through `m.Update`; the viewport content contains the new text.
  3. `TestPluginToastSeverity`: `ToastMsg` tones `""`→`setStatus`, `warn`→`setNotice`, `error`→`setFailure` (assert `m.statusSeverity` values used by those setters).
  4. `TestPopupModeSetter`.
- [ ] **Step 2: Model side.** Add fields `pluginHost *plugin.Host`, `pluginSyncHash string`, `popupMode bool`; setters/accessor in `model_modes.go`; `plugin_host.go` holds `syncPlugins`, the `pluginSyncMsg` type, and `handlePluginMsg(msg tea.Msg) (Model, tea.Cmd, bool)` covering:
  - `plugin.StateChangedMsg`: clear pending plugin actions for `msg.Beads` (Task 5 adds the map; leave a `// pending actions are cleared in clearPluginPending` call that is a no-op until 5 — implement `clearPluginPending` here as an empty method 5 fills); `m.updateViewportContent()` when the detail pane is visible; if `m.filter.activeBQLExpr != nil` and the active query text contains any of `m.pluginHost.FieldPrefixes()`, re-run it with the existing reapply path (`m.applyBQL(m.filter.activeBQLExpr, <query text>)` — find where the query text is stored; keep the cursor on the same bead if the existing helper does).
  - `plugin.ToastMsg`: map tone to `setStatus` / `setNotice` / `setFailure`, message prefixed with nothing (plugins write their own wording).
  - `plugin.StatusMsg`: `failed` → `setFailure("Plugin <name> failed: <LastError>")`; `active` after a restart → `setStatus("Plugin <name> restarted")`; first activation silent.
  - `plugin.PromptMsg`: forwarded to Task 5's prompt modal; until 5, reply `nil` immediately.
  Call `handlePluginMsg` at the top of `Update`'s type switch. Call `m.syncPlugins()` at the end of `handleSnapshotReady` and `handleDataSourceReload` (before their final return), and handle `pluginSyncMsg`.
- [ ] **Step 3: cmd/bt.** In `root.go`:
  - `var flagPopup bool`; `f.BoolVar(&flagPopup, "popup", false, "Quit after a plugin action asks to (for terminal popups)")`.
  - In the normal TUI branch after the existing setters: `configs, err := plugin.LoadConfig(path)`; on error `debug.Log` and continue; if any enabled config: build `plugin.NewHost(plugin.Options{Configs, BTVersion: version.Version (check pkg/version for the exported name), Scope, Popup: flagPopup, DB, Repo})` and `m.SetPluginHost(host)`. `m.SetPopupMode(flagPopup)` always.
  - Scope: `global` when `appCtx.selectedSource != nil && appCtx.selectedSource.Type == datasource.SourceTypeDoltGlobal` (Databases = distinct `SourceRepo` of `appCtx.issues`), `workspace` when `appCtx.workspaceInfo != nil` and not global, else `project`.
  - `DB`: the `beadDB` closure from `docs/superpowers/notes/bt-host-integration.md` §4 (project DB from `detectCurrentProjectDB()`).
  - `Repo`: `appCtx.routeTable.Resolve(*issue)` → `Dir` on success, cached per DB in a mutex-guarded map (cache misses too, for 30s).
  - `runTUIProgram`: after `p := tea.NewProgram(...)` and `coalescer.SetSender(p.Send)`: `if h := m.PluginHost(); h != nil { h.SetSender(p.Send); h.Start(); defer h.Stop() }`.
  - Never build a host in the `--as-of` branch or robot paths.
- [ ] **Step 4: Build** `go build -o /tmp/bt-plugin ./cmd/bt` must succeed (the controller runs the interactive check in Part C).
- [ ] **Step 5: Run** focused pkg/ui tests (HOME override), `go build ./...`, `go vet ./...`. **Commit** `feat(tui): run configured plugins in the TUI`.

---

### Task 5: Plugin actions, action menu, prompts, pending spinner, popup dismiss

**Files:**
- Create: `pkg/ui/plugin_actions.go`, `pkg/ui/plugin_prompt.go`, `pkg/ui/plugin_actions_test.go`
- Modify: `pkg/ui/model.go` (ModalType constants appended at the end of the const block: `ModalPluginPrompt`; focus constant `focusPluginPrompt`; Model field `pluginPrompt *pluginPrompt`), `pkg/ui/model_update_input.go` (modal key block after the existing modal blocks; `tryPluginActionKey` call between the global switch end and `switch m.focused`), `pkg/ui/model_view.go` (overlay), `pkg/ui/model_modes.go` (focus name), `pkg/ui/claim.go` (`writePluginAction` kind; expiry on spinner tick), `pkg/ui/model_footer.go` (`modalKeyMap` case), `pkg/ui/context.go` if it maps modals.

**Interfaces — Consumes:** `Host.Actions`, `Host.Invoke`, `plugin.Action`, `plugin.ActionResult`, `plugin.PromptMsg` (3); `m.pendingWrites`, `writeSpinnerTickCmd`, `setStatus/setNotice/setFailure`, `renderFieldModalLines`, `OverlayCenterDimBackdrop` (existing).

**Behaviour (spec + "First local version"):**
- `selectedIssue() *model.Issue`: list (`ViewList`, focus list or detail) → `m.data.issueMap[selected ID]`; board → `m.board.SelectedIssue()`; tree → `m.tree.SelectedIssue()`; epics → `cursorRow().issue` when not a project header. Else nil.
- `tryPluginActionKey(msg)`: only when `m.pluginHost != nil`, `m.activeModal == ModalNone`, view/focus is one of the four (board not in search mode), and the key is not matched by any binding of `m.keys.Global` or the view's own key map (flatten `viewSpecificKeyMap().FullHelp()`; for the list also the bubbles list paging keys `b u d f g G`). Key `P` (when not bound by bt) opens the action menu. Otherwise, if the selected bead has an action whose `Key` equals `msg.String()`, invoke it. Returns `(Model, tea.Cmd, bool)`.
- Invoke: refuse with `setNotice("<label> already running on <id>")` if `m.pendingWrites[id]` exists; else add `pendingWrite{Kind: writePluginAction, Field: action.Label, StartedAt: now}`, start the spinner tick if inactive, `setNotice("<Label> <id>…")` when not in the list view, and return a `tea.Cmd` running `host.Invoke(context.Background(), a, issueCopy, view)` → `pluginActionResultMsg{ID, Action, Result}`.
- `pluginActionResultMsg`: delete pending; close an open plugin prompt of that plugin; `Unknown` → `setFailure("<label> on <id>: result unknown")`; toast → tone mapping as in 4; nothing → `setStatus("<label> done")`; `Dismiss && m.popupMode` → `tea.Quit`.
- `clearPluginPending(keys)`: delete pending entries of kind `writePluginAction` whose ID is in keys (match by issue ID).
- Spinner tick: also expire entries older than `writeSettleTimeout` (`setFailure("<label> on <id>: no confirmation after 45s")`).
- Action menu: a `pluginPrompt{kind: menu, options: actions for the selected bead with label and key}` shown with the select rendering; Enter invokes the chosen action; Esc closes. Empty → `setNotice("No plugin actions for <id>")`.
- Prompts from plugins (`PromptMsg`): confirm → `pluginPrompt{kind: confirm}`: `y`/`Y`/`enter` → `Reply(true)`, `n`/`N`/`esc` → `Reply(false)`, `ctrl+c` → `tea.Quit` (reply nil first); select → `j`/`k`/up/down move, enter → `Reply(option.Value)`, esc → `Reply(nil)`. Closing restores the previous focus stored in the prompt. If another prompt is open, reply nil to the new one immediately.
- Rendering: `renderPluginPrompt()` using `renderFieldModalLines(title, lines, m.theme, maxInner)`; select lines like `FieldPickerModal.View` (`> ` cursor, label, dim description, hint `j/k move  enter select  esc cancel`); confirm lines: message wrapped, hint `y/enter confirm  n/esc cancel` (use the provided labels when set). Overlay with `OverlayCenterDimBackdrop(body, panel, m.width, m.height-1)` after background renders.

Steps:
- [ ] **Step 1: Tests** (`plugin_actions_test.go`, HOME override). Use a fake action source: make `tryPluginActionKey` and the menu read actions through a small interface on the model (`pluginActions interface { Actions(*model.Issue) []plugin.Action; Invoke(context.Context, plugin.Action, *model.Issue, string) plugin.ActionResult }`) that `SetPluginHost` sets to the host and tests set to a fake. Tests:
  1. `D` on the selected list bead invokes `dispatch` (fake records the call after running the returned Cmd) and the row shows the spinner (`m.pendingWrites` has the ID).
  2. `D` while the list filter is typing does nothing; `D` while the BQL modal is open does nothing.
  3. A plugin action declaring key `j` is never invoked (bt's binding wins) but is listed in the menu.
  4. `P` opens the menu; `j`, `enter` invokes the second action.
  5. Result with `Dismiss` quits only when `popupMode` (assert returned cmd is `tea.Quit`: run it and check for `tea.QuitMsg`).
  6. `Unknown` result shows a failure mentioning "result unknown" and clears pending.
  7. `PromptMsg` confirm: `y` calls Reply(true) and restores focus; select: `j` then `enter` replies the second value; `esc` replies nil.
  8. Spinner expiry after `writeSettleTimeout` (set `StartedAt` in the past, send a spinner tick).
  9. `selectedIssue` in board, tree, epics (child row) and epics header (nil).
- [ ] **Step 2: Implement** to the behaviour above.
- [ ] **Step 3: Run** focused tests, then the pkg/ui suite (HOME override), `go build ./...`, `go vet ./...`, key tests `go test ./pkg/ui/keys/`.
- [ ] **Step 4: Commit** `feat(tui): plugin actions, action menu and prompts`

---

### Task 6: `bt plugins` and docs

**Files:**
- Create: `cmd/bt/cobra_plugins.go`, `cmd/bt/cobra_plugins_test.go`, `docs/plugins.md`
- Modify: `README.md` (one "Plugins" subsection linking docs/plugins.md), `CHANGELOG.md` (entry), `AGENTS.md` (one line under TUI slots pointing at `pkg/plugin`)

- [ ] **Step 1:** `bt plugins` (pattern of `cobra_hooks.go`): loads `plugin.LoadConfig(plugin.DefaultConfigPath())`, prints one line per plugin: name, enabled/disabled, command, resolved binary (`exec.LookPath(cmd[0])` or "not found"), key overrides; `--json` prints the same as JSON. Config errors print and exit non-zero. Test with a temp HOME config (two plugins, one disabled, one missing binary).
- [ ] **Step 2:** `docs/plugins.md`: configuration (yaml example with `name: example`), lifecycle, transport, every method with an example message (copy the protocol section of the spec with `example`/`proj` names), manifest validation rules, keys and the `P` menu, `--popup`, failure behaviour. Must not name tsk, OpenCode or herdr.
- [ ] **Step 3:** README/CHANGELOG/AGENTS edits; full verification: `go build ./...`, `go vet ./...`, gofmt on branch-changed files, `go test ./...` (pkg/ui with HOME override).
- [ ] **Step 4: Commit** `feat(cli): bt plugins command` and `docs(docs): plugin protocol reference`.

---

## Part B — tsk

### Task 7: Worktree

- [ ] **Step 1**

```bash
cd /home/a.simard@multidev.local/gits/taskestrator
git status --short    # must be clean
git worktree add .worktrees/bt-plugin -b feat/bt-plugin main
cd .worktrees/bt-plugin && make build && make test
```

---

### Task 8: Adapter additions (Show, question text, herdr probe)

**Files:**
- Modify: `internal/tracker/tracker.go` (+ `Show` on interface and `BD`), `internal/tracker/tracker_test.go`
- Modify: `internal/status/status.go` (`Snapshot.Question string`), `internal/opencode/client.go` (`FirstQuestion`), `internal/opencode/snapshot.go`, `internal/opencode/client_test.go`
- Modify: `internal/herdr/herdr.go` (`Running(ctx) bool` on `CLI`, not on the interface), `internal/herdr/herdr_test.go`
- Modify: every `tracker.Tracker` fake (`internal/dispatch/dispatch_test.go` `recorder`; `internal/ui/helpers_test.go` `fakeTracker` — it is deleted in 13 but must compile until then)

**Interfaces — Produces:**
- `Tracker.Show(ctx, repo, id string) (Task, error)` — runs `bd show <id> --json`, which prints a one-element JSON array (verified 2026-10-07); empty array → error `bd show <id>: not found`.
- `status.Snapshot.Question string` — text of the first pending question in the session tree, "" if none.
- `(*opencode.Client).FirstQuestion(ctx, id string) (string, error)` — `GET /api/session/{id}/form`; returns `fields[0].title` of the first form whose `metadata.kind == "question"` (fall back to the form `title` when there are no fields); "" when none.
- `Snapshot` sets `Question` from the first non-empty `FirstQuestion` while walking the tree (only for nodes whose form count > 0, to avoid extra requests).
- `herdr.CLI.Running(ctx) bool` — runs `herdr status server`; true when exit 0 and output contains `status: running`.

- [ ] Tests first for each (table tests; `client_test.go`'s `world` handler gains a form with fields `[{title:"Which DB?"}]`; herdr via its `Exec` runner fake), see them fail, implement, pass. `make test lint`. Commit `feat: tracker Show, question text in snapshots, herdr probe`.

---

### Task 9: Protocol and connection (tsk side)

**Files:**
- Create: `internal/btplugin/protocol.go`, `internal/btplugin/rpc.go`, `internal/btplugin/rpc_test.go`

**Interfaces — Produces:** the same wire format as Part A Tasks 1 and 2, written independently:
- Types `Bead{DB, ID string; Repo *string; Status, Title, Type string; Priority int; Assignee string; Metadata map[string]json.RawMessage}`, `BeadKey{DB, ID}`, `BeadRef{DB, ID string; Repo *string}`, `InitializeParams{ProtocolVersion int; Scope{Mode string; Databases []string}; Popup bool; Options map[string]any}`, `Manifest` (+ `Subscribe`, `Field`, `FieldValue{ID, Label, Badge, Tone string; Lane *bool}`, `SectionDecl`, `ActionDecl`), `SyncParams{Revision int; Beads []Bead}`, `BeadState{DB, ID; Fields map[string]FieldState; Sections map[string]string; Actions []string}`, `FieldState{Value string; Since *time.Time}`, `StateSetParams{Replace bool; Beads []BeadState}`, `StateClearParams{Beads []BeadKey}`, `InvokeParams{Action string; Bead BeadRef; View string}`, `InvokeResult{Toast *Toast; Dismiss bool}`, `Toast{Message, Tone string}`. JSON tags exactly as the spec's examples (`protocolVersion`, `metadata_prefixes`, `repo` always present, `since` omitempty).
- `func Manifest() Manifest` — tsk's manifest per Global Constraints.
- `type Peer struct` with `NewPeer(r io.Reader, w io.Writer, h func(ctx, method string, params json.RawMessage) (any, error)) *Peer`, `Run(ctx) error`, `Notify(method string, params any) error`, `Call(ctx, method string, params, result any) error`; errors: `type RPCError struct{Code int; Message string; Data json.RawMessage}`, `func Fail(typ, msg string) *RPCError` (code -32000). Requests from bt run in their own goroutine; notifications in order; 4 MiB lines.

- [ ] Tests: round trip of a request handled by tsk (fake bt writes `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{…}}`, reads the manifest back and checks `name`, `actions[0].key == "D"`, `fields[0].values` order and `lane:false` on `queued`/`unknown`); notification order; `Fail` produces code -32000 with `data.type`; `state.set` encoding has `"repo":null` absent (BeadState has no repo) and `since` RFC3339. Implement; `make test lint`. Commit `feat: bt plugin protocol for tsk`.

---

### Task 10: State engine

**Files:**
- Create: `internal/btplugin/engine.go`, `internal/btplugin/engine_test.go`

**Interfaces — Consumes:** `status.Derive`, `status.Snapshot` (with `Question`), `tracker.MetaSession/MetaWorktree/MetaBranch`.

**Interfaces — Produces:**

```go
type Env struct {
	HerdrUp      bool
	RepoFor      func(b Bead) string // resolved checkout ("" unknown): b.Repo or a roots scan by DB
}
type Engine struct{ /* mutex-guarded */ }
func NewEngine(now func() time.Time) *Engine
// Sync replaces the bead set. Returns the pushes needed and the session IDs
// that appeared (to load snapshots for).
func (e *Engine) Sync(beads []Bead, env Env) (set []BeadState, clear []BeadKey, newSessions []string)
// SetSnapshots records snapshots (nil value = failed to load). reachable=false
// marks every session unknown. Returns the pushes needed.
func (e *Engine) SetSnapshots(snaps map[string]*status.Snapshot, reachable bool, env Env) (set []BeadState, clear []BeadKey)
// All returns the full current state for a replace push.
func (e *Engine) All(env Env) []BeadState
func (e *Engine) Sessions() []string             // sessions of live beads (have tsk.session, not closed/review)
func (e *Engine) Bead(k BeadKey) (Bead, bool)
func (e *Engine) Display(k BeadKey) (status.Display, bool)
func (e *Engine) Reachable() bool
```

Mapping (spec Part 2 "State mapping"; Display from `status.Derive(bead.Status, meta(tsk.session), snap)` where snap is nil when unreachable or not loaded):
- Done, Untracked → clear (and forget `since`).
- Review → `review`, actions none.
- Queued → `queued`, `[dispatch]` if `env.RepoFor(b) != "" && env.HerdrUp`.
- Lost → `lost`, same dispatch rule.
- Waiting → `waiting`, `[jump]` if HerdrUp. Detail `question`/`permission`/`idle`.
- InProgress → `running`, `[jump]` if HerdrUp.
- Unknown → `unknown`, `[jump]` if HerdrUp.
- `since`: kept while the value is unchanged; set to `now()` when the value changes or the bead is first seen.
- Section `agent` (only for beads with a session): lines `**Session** \`<id>\``, `**Worktree** \`<path>\``, `**Branch** \`<branch>\`` (omit missing), blank line, then for waiting: `Waiting on you: question — <Question>` / `permission` / `idle`; for lost: `Session not found in OpenCode; dispatch again with D.`; for unknown: `OpenCode service unreachable.`
- Diffing: a push entry is emitted only when its encoded BeadState differs from the last one pushed for that key; clears only for keys previously pushed. Beads missing from a new `Sync` that were pushed before → clear.
- Metadata values are JSON strings; decode `json.RawMessage` to string, ignore non-strings.

- [ ] Tests (table-driven, fixed clock): each mapping row; dispatch omitted without repo or herdr; since kept on unchanged value and reset on change; second identical Sync emits nothing; removed bead cleared once; unreachable → all live beads `unknown` and recover to `running` on reachable snapshots; agent section contents for waiting-question including the question text; `newSessions` lists only sessions not seen before. Implement; `make test lint`. Commit `feat: tsk plugin state engine`.

---

### Task 11: Live loop

**Files:**
- Create: `internal/btplugin/live.go`, `internal/btplugin/live_test.go`

**Interfaces — Consumes:** `opencode.Client` methods `Active`, `Snapshot`, `Root`, `Subscribe`, `opencode.Relevant`; `opencode.FromService`.

**Interfaces — Produces:**

```go
type Live interface {
	Active(ctx context.Context) (map[string]bool, error)
	Snapshot(ctx context.Context, id string, active map[string]bool) (status.Snapshot, error)
	Root(ctx context.Context, id string) (string, error)
	Subscribe(ctx context.Context) (<-chan opencode.Event, error)
}
type Loop struct {
	Connect  func(ctx context.Context) (Live, error) // opencode.FromService, retried lazily
	Sessions func() []string                         // Engine.Sessions
	Update   func(snaps map[string]*status.Snapshot, reachable, full bool)
	// Timers (package defaults, overridable in tests)
	Debounce time.Duration      // 300ms
	Backoff  []time.Duration    // 1s,2s,4s,8s,16s,30s (then 30s)
}
func (l *Loop) Run(ctx context.Context)          // until ctx done
func (l *Loop) Refresh(ids ...string)            // load these sessions soon (non-blocking)
```

Behaviour (ported from `internal/ui/board.go` + `live.go`, see notes §2): connect (on failure `Update(nil, false, true)`, back off, retry); subscribe; on success full reload: `Active` + `Snapshot` for every `Sessions()` (10s timeout; per-session error → nil entry) then `Update(snaps, true, true)`; for each event with `SessionID != ""` and `Relevant(Type)`: `Root` (10s; error → drop), debounce per root, then reload that root and `Update({root: snap}, true, false)`; `Refresh(ids)` reloads those ids the same way; stream closed → `Update(nil, false, true)` only if the reconnect fails, back off, reconnect, full reload. `Active` error during a reload → `Update(nil, false, true)`.

- [ ] Tests with a fake `Live` whose `Subscribe` returns a test-controlled channel and whose snapshots/active/parents are maps: (1) full reload on start; (2) event on a child session reloads its root once after debounce even when 5 events arrive; (3) channel close → reconnect → full reload; (4) Connect failing twice then succeeding → first Updates have reachable=false, then true (Review Focus 5); (5) `Refresh` loads a new session. Use short timers. Implement; `make test lint` (`-race`). Commit `feat: tsk plugin live loop`.

---

### Task 12: Actions

**Files:**
- Create: `internal/btplugin/actions.go`, `internal/btplugin/actions_test.go`

**Interfaces — Consumes:** `dispatch.Run`, `dispatch.Deps`, `dispatch.Request`, `dispatch.Jump`, `dispatch.TargetFor`, `discover.Project`, `config.LoadProjectFile`, `tracker.Tracker` (`Show`, `Ready`, `HasReviewStatus`), `gitwt.Git.IsIgnored`, `Engine` (10).

**Interfaces — Produces:**

```go
type Actions struct {
	Engine   *Engine
	Tracker  tracker.Tracker
	Git      gitwt.Git
	Deps     func() (dispatch.Deps, error) // builds deps; errors when OpenCode is unreachable
	Herdr    herdr.Herdr                    // nil when herdr unavailable
	HerdrUp  func() bool
	RepoFor  func(b Bead) string
	OnDispatched func(sessionID string)     // Loop.Refresh
}
func (a *Actions) Invoke(ctx context.Context, p InvokeParams) (InvokeResult, error)
```

- `dispatch`: look up the bead (`Engine.Bead`); errors as `Fail(type, message)`:
  - unknown bead → `unknown_bead`; display not queued/lost → `not_dispatchable` ("<id> is <state>");
  - repo "" → `no_repo` ("no checkout found for <db>; add its parent to roots");
  - herdr down → `no_herdr` ("herdr is not running");
  - `Ready` does not contain the id (when queued) → `not_ready` ("<id> is blocked");
  - `HasReviewStatus` false → `no_review_status` with the `bd config set status.custom "review:wip"` hint;
  - `.worktrees` not ignored (`IsIgnored(repo, ".worktrees/tsk-check")`) → `worktrees_not_ignored`;
  - in flight for the same key → `busy`;
  - then `Show` the task, `config.LoadProjectFile(repo)`, `Deps()`, `dispatch.Run(ctx, deps, Request{Project{Name: db, Repo: repo}, task, file})`; error → `dispatch_failed` with the joined step error; success → toast `Dispatched <id> (session <sid>)` (append `; tab not opened: <err>` when `TabError`), call `OnDispatched(sid)`.
- `jump`: needs `tsk.session` (else `not_dispatched`); herdr down → `no_herdr`; build `Target` with `dispatch.TargetFor(Project{Name: db, Repo: repo-or-derived}, task-from-bead)` (repo unknown → derive from `tsk.worktree` by cutting `/.worktrees/<id>`); `dispatch.Jump`; success → `InvokeResult{Dismiss: true}`.
- Unknown action → `unknown_action`.

- [ ] Tests with fakes (copy the `recorder` pattern from `internal/dispatch/dispatch_test.go` into `internal/btplugin/fakes_test.go`; do not import test files): each refusal type; happy dispatch calls Run steps and `OnDispatched`; jump returns Dismiss and calls Focus; jump with repo derived from worktree. Implement; `make test lint`. Commit `feat: tsk plugin dispatch and jump actions`.

---

### Task 13: `tsk plugin` server and command; retire the TUI

**Files:**
- Create: `internal/btplugin/server.go`, `internal/btplugin/server_test.go`
- Modify: `cmd/tsk/main.go` (only `plugin` subcommand), `internal/config/config.go` (+ tests: drop `Config`, `Load`, `DefaultPath`, `Default`; keep `ProjectFile`, `LoadProjectFile`, `ExpandHome`), `Makefile` if it references removed packages, `go.mod` (`go mod tidy` drops bubbletea)
- Delete: `internal/ui/` and `internal/board/` (tsk's own packages; this repo allows it), their tests.
- Modify: `README.md`, `AGENTS.md`, `docs/contract-check.md` (note), `~/.config/herdr/config.toml` is NOT touched by the subagent — the hand-off lists the change for the user.

**Interfaces — Produces:**

```go
type Server struct {
	Peer    *Peer
	Engine  *Engine
	Loop    *Loop
	Actions *Actions
	Roots   []string                 // from initialize options.roots; default ~/gits
	Scan    func(roots []string) []discover.Project
	HerdrRunning func() bool
	Tracker tracker.Tracker
}
func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
func Main(ctx context.Context, in io.Reader, out io.Writer) error // builds real deps and runs until stdin closes or shutdown
```

Behaviour:
- `initialize` → read `options.roots` (strings; `~` expanded), scan projects lazily on first need, reply `Manifest()`; start the `Loop` goroutine.
- `beads.sync` → `HerdrUp = HerdrRunning()` (once per sync), `Engine.Sync`, push `state.set` (non-empty) then `state.clear` (non-empty), `Loop.Refresh(newSessions...)`; per database seen for the first time with a resolved repo: `HasReviewStatus` false → `ui.toast` warn: `<db> has no "review" status; run in <repo>: bd config set status.custom "review:wip"` (once per db per process).
- `Loop.Update` callback → `Engine.SetSnapshots` → pushes; `full` → push `state.set` with `replace: true` and `Engine.All(env)`; a transition to unreachable sends one `ui.toast` error: `OpenCode service unreachable; start it with: opencode service start` (again only after it was reachable in between).
- `action.invoke` → `Actions.Invoke`.
- `shutdown` → cancel ctx; `Main` returns nil when stdin closes.
- Nothing is written to stdout except protocol messages; logs go to stderr, prefixed `tsk:`; the password is never logged.
- `cmd/tsk`: `tsk plugin` → `btplugin.Main(ctx, os.Stdin, os.Stdout)`; no other subcommands; usage text says so. `tsk` with no args prints usage and the bt config snippet.

- [ ] Tests (`server_test.go`): drive `Server.Handle` with a fake peer (record notifications) and fake engine deps: initialize returns the manifest and starts the loop (fake Loop.Run records); sync pushes set then clear in that order; the review-status toast is sent once per db; unreachable toast once per transition; replace push on full update. Implement, delete `internal/ui` and `internal/board`, update `cmd/tsk`, `config`, docs; `go mod tidy`; `make build test lint`. Commit `feat: tsk plugin command; remove tsk's own TUI`.

---

### Task 14: End-to-end check with real services

**Files:**
- Modify: `e2e/dispatch_test.go` (keep the existing test) + Create: `e2e/plugin_test.go` (`//go:build e2e`)

- [ ] `TestPluginEndToEnd`: build `tsk` into a temp dir; create the same throwaway Beads repo the existing e2e uses; start `tsk plugin` as a subprocess; speak the protocol as bt would (initialize with `roots` = the temp parent, `beads.sync` with the throwaway bead and `repo` set); expect `state.set` `queued` with `[dispatch]` (requires herdr running; skip with a clear message otherwise); `action.invoke dispatch` → toast `Dispatched`; send a new `beads.sync` with the bead's metadata after `bd show` (session set) → expect `running` or `waiting` within 60s; `action.invoke jump` → `dismiss:true`; cleanup like the existing e2e (delete session, remove worktree, close bead). Run `make e2e` and record the output. Commit `test: e2e for tsk plugin`.

---

## Part C — Hand-off (controller, not a subagent)

- [ ] Build both: `go build -o /tmp/bt-plugin ./cmd/bt` (bt worktree) and `make build` (tsk worktree → `bin/tsk`).
- [ ] Ask the user before: replacing `~/go/bin/bt` (upstream build) with the local build, `make install` for tsk, writing `~/.config/bt/config.yaml`'s `plugins:` entry, and changing the herdr popup keybind from `tsk board` to `bt -g --popup`. Provide the exact snippets:

```yaml
# ~/.config/bt/config.yaml
plugins:
  - name: tsk
    command: ["tsk", "plugin"]
    options:
      roots: ["~/gits"]
```

```toml
# ~/.config/herdr/config.toml
[[keys.command]]
type = "popup"
command = "bt -g --popup"
```
