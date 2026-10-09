# Session Link Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Link OpenCode sessions to Beads tasks their agents claim outside `D`, automatically (OpenCode plugin + `tsk link`) and manually (bt action `K`), so those tasks get live state and jump in bt.

**Architecture:** All linking logic lives in a new Go package `internal/link`: a command parser that finds `bd update … --claim` segments, and one link rule that writes `tsk.session` / `tsk.worktree`. `tsk link` (CLI) runs it for the OpenCode plugin `opencode/tsk-link.js`, which hooks `tool.execute.after`. The bt plugin pushes a new `untracked` state for in-progress beads without a session and offers a `link` action that picks an OpenCode pane from `herdr agent list` and applies the same rule.

**Tech Stack:** Go 1.27 (stdlib only), plain JavaScript ESM for the OpenCode v2 plugin, `node --test` (Node 26) for its tests.

**Spec:** `docs/superpowers/specs/2026-10-09-session-link-design.md`

**Worktree:** `/home/a.simard@multidev.local/gits/taskestrator/.worktrees/session-link`, branch `feat/session-link`. Run every command there.

## Global Constraints

- No new Go module dependencies; the JS plugin uses only Node built-ins (`node:child_process`) and has no `package.json`.
- `tsk plugin` must never print to stdout except protocol messages; `tsk link` prints only to stderr and its log.
- Never log, toast or store the OpenCode service password.
- Metadata keys are `tracker.MetaSession` (`tsk.session`) and `tracker.MetaWorktree` (`tsk.worktree`); linking never writes `tsk.branch`.
- Commit messages: `feat: …` / `test: …` / `docs: …`; the body ends with the trailer `Task: tsk-jqp`.
- After each task: `make test` and `make lint` clean (`make lint` = `go vet ./...` + `gofmt -l .` empty).
- Never push. Never run `make install`, create the plugin symlink, or restart the OpenCode service without the user's explicit go-ahead (Task 9).

## Review Focus

1. A claim whose quoted text contains shell operators (`--notes "a && b; c"`) must still be found, and only once — Task 1 test `quoted operators`.
2. A pane in a sibling directory sharing the checkout's prefix (`/repository` next to `/repo`) must not be offered by `K` — Task 5 `TestWithin` and Task 7 `TestLinkHappy` (the `ses_far` pane).
3. OpenCode down when the plugin fires: `tsk link` exits 1, logs one error line per claimed task, writes nothing to bd — Task 3 `TestMainConnectFails`.
4. An agent in a session started by `D` claims its own task again: result `same`, no bd write — Task 2 `same session`.
5. The claim is made by a subagent: the root session is linked, because herdr and the engine follow the root — Task 2 `links the root of a child session`.

---

### Task 1: Claim parser

**Files:**
- Create: `internal/link/parse.go`
- Test: `internal/link/parse_test.go`

**Interfaces:**
- Produces: `type Claim struct { Dir string; IDs []string }`, `func ParseClaims(dir, command string) []Claim` (nil when there is no claim), unexported `resolve(dir, p string) string` (used by Task 3).

- [ ] **Step 1: Write the failing test**

`internal/link/parse_test.go`:

```go
package link

import (
	"reflect"
	"testing"
)

func TestParseClaims(t *testing.T) {
	one := func(dir string, ids ...string) []Claim { return []Claim{{Dir: dir, IDs: ids}} }
	tests := []struct {
		name, cmd string
		want      []Claim
	}{
		{"claim", "bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"status flag", "bd update tsk-a1b --status in_progress", one("/r", "tsk-a1b")},
		{"status equals", "bd update tsk-a1b --status=in_progress", one("/r", "tsk-a1b")},
		{"short status", "bd update tsk-a1b -s in_progress", one("/r", "tsk-a1b")},
		{"quoted status", `bd update tsk-a1b --status "in_progress"`, one("/r", "tsk-a1b")},
		{"several and dotted ids", "bd update tsk-a1b tsk-c2d.1 --claim", one("/r", "tsk-a1b", "tsk-c2d.1")},
		{"chain", "git pull && bd update tsk-a1b --claim; echo ok", one("/r", "tsk-a1b")},
		{"pipe and or", "true || bd update tsk-a1b --claim | cat", one("/r", "tsk-a1b")},
		{"newline", "echo hi\nbd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"cd relative", "cd sub && bd update tsk-a1b --claim", one("/r/sub", "tsk-a1b")},
		{"cd absolute", "cd /x/y && bd update tsk-a1b --claim", one("/x/y", "tsk-a1b")},
		{"dash C", "bd -C ../other update tsk-a1b --claim", one("/other", "tsk-a1b")},
		{"directory equals", "bd --directory=/x update tsk-a1b --claim", one("/x", "tsk-a1b")},
		{"global value flag", "bd --actor alice update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"assignment and path", "BD_X=1 /usr/local/bin/bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"quoted operators", `bd update tsk-a1b --claim --notes "a && b; c | d"`, one("/r", "tsk-a1b")},
		{"flag value looks like an id", "bd update tsk-a1b --claim --notes fix-this", one("/r", "tsk-a1b", "fix-this")},
		{"duplicate", "bd update tsk-a1b --claim && bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"two dirs", "bd update tsk-a1b --claim && cd sub && bd update tsk-a1b --claim", []Claim{
			{Dir: "/r", IDs: []string{"tsk-a1b"}}, {Dir: "/r/sub", IDs: []string{"tsk-a1b"}},
		}},
		{"show", "bd show tsk-a1b", nil},
		{"close", "bd close tsk-a1b", nil},
		{"claim only in a quoted note", `bd update tsk-a1b --notes "--claim later"`, nil},
		{"echo", "echo bd update tsk-a1b --claim", nil},
		{"other status", "bd update tsk-a1b -s open", nil},
		{"no id", "bd update --claim", nil},
		{"other database", "bd --db other update tsk-a1b --claim", nil},
		{"global database", "bd update tsk-a1b --claim --global", nil},
		{"lookalike command", "bdx update tsk-a1b --claim", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseClaims("/r", tt.cmd); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseClaims(%q) = %#v, want %#v", tt.cmd, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/link/`
Expected: FAIL, `undefined: Claim` / `undefined: ParseClaims`.

- [ ] **Step 3: Write the implementation**

`internal/link/parse.go`:

```go
// Package link links OpenCode sessions to the Beads tasks their agents claim.
package link

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Claim is a bd update that claims tasks, with the directory it ran in.
type Claim struct {
	Dir string
	IDs []string
}

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]*-[A-Za-z0-9._]+$`)
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// bd global flags that take a value, other than -C/--directory.
	valueFlags = map[string]bool{"--actor": true, "--dolt-auto-commit": true, "--mem-profile": true}
	// bd global flags that point bd at another database.
	otherDB = map[string]bool{"--db": true, "--database": true, "--global": true}
)

// ParseClaims finds the bd update commands in command that claim tasks.
// dir is the directory the command starts in. Each (directory, ID) appears
// once.
func ParseClaims(dir, command string) []Claim {
	var claims []Claim
	seen := map[string]bool{}
	for _, words := range segments(command) {
		if words[0] == "cd" {
			if len(words) > 1 {
				dir = resolve(dir, words[1])
			}
			continue
		}
		c, ok := claimOf(dir, words)
		if !ok {
			continue
		}
		var ids []string
		for _, id := range c.IDs {
			if k := c.Dir + "\x00" + id; !seen[k] {
				seen[k] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			claims = append(claims, Claim{Dir: c.Dir, IDs: ids})
		}
	}
	return claims
}

// resolve returns p relative to dir, expanding a leading ~.
func resolve(dir, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// segments splits command into simple commands on ;, &, |, and newlines
// outside quotes, and each command into words with quotes and backslash
// escapes removed. Empty commands are dropped.
func segments(command string) [][]string {
	var segs [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '\'':
			inWord = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
			}
		case c == '\\':
			if i+1 < len(rs) {
				i++
				if rs[i] != '\n' {
					cur.WriteRune(rs[i])
					inWord = true
				}
			}
		case c == ';' || c == '&' || c == '|' || c == '\n':
			endSegment()
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	endSegment()
	return segs
}

// claimOf reports the claim made by one simple command, if any.
func claimOf(dir string, words []string) (Claim, bool) {
	i := 0
	for i < len(words) && assignment.MatchString(words[i]) {
		i++
	}
	if i >= len(words) || filepath.Base(words[i]) != "bd" {
		return Claim{}, false
	}
	// bd's global flags come before the subcommand.
	for i++; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
		name, value, hasValue := strings.Cut(words[i], "=")
		switch {
		case otherDB[name]:
			return Claim{}, false
		case name == "-C" || name == "--directory":
			if !hasValue {
				if i+1 >= len(words) {
					return Claim{}, false
				}
				i++
				value = words[i]
			}
			dir = resolve(dir, value)
		case valueFlags[name] && !hasValue:
			i++
		}
	}
	if i >= len(words) || words[i] != "update" {
		return Claim{}, false
	}
	rest := words[i+1:]
	claim := false
	var ids []string
	for j := 0; j < len(rest); j++ {
		w := rest[j]
		name, _, _ := strings.Cut(w, "=")
		switch {
		case w == "--claim", w == "--status=in_progress", w == "-s=in_progress":
			claim = true
		case (w == "--status" || w == "-s") && j+1 < len(rest):
			j++
			if rest[j] == "in_progress" {
				claim = true
			}
		case otherDB[name]:
			return Claim{}, false
		case !strings.HasPrefix(w, "-") && idPattern.MatchString(w):
			ids = append(ids, w)
		}
	}
	if !claim || len(ids) == 0 {
		return Claim{}, false
	}
	return Claim{Dir: dir, IDs: ids}, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/link/ && make lint`
Expected: PASS, lint clean.

- [ ] **Step 5: Commit**

```bash
git add internal/link/parse.go internal/link/parse_test.go
git commit -m "feat: parse bd claims out of shell commands

Task: tsk-jqp"
```

---

### Task 2: The link rule (and `tracker.ErrNotFound`)

**Files:**
- Modify: `internal/tracker/tracker.go` (`Show`, new `ErrNotFound`, new `notFound`)
- Modify: `internal/tracker/tracker_test.go` (fake returns stdout with errors; new tests)
- Create: `internal/link/link.go`
- Test: `internal/link/link_test.go`

**Interfaces:**
- Consumes: `tracker.Task`, `tracker.Task.Meta`, `tracker.MetaSession`, `tracker.MetaWorktree`; `opencode.Session` (`ID`, `ParentID`, `Title`, `Location.Directory`), `opencode.ErrNotFound`.
- Produces:
  - `tracker.ErrNotFound` (message `not found`), returned wrapped by `BD.Show` when bd has no such task.
  - `link.Tracker` interface `{ Show(ctx, repo, id string) (tracker.Task, error); SetMetadata(ctx, repo, id string, kv map[string]string) error }` (`tracker.BD` satisfies it).
  - `link.OpenCode` interface `{ Root(ctx, id string) (string, error); GetSession(ctx, id string) (opencode.Session, error) }` (`*opencode.Client` satisfies it).
  - `type Result string` with `Linked`, `Same`, `NotFound`, `NotInProgress`, `LiveSession`.
  - `type Outcome struct { Result Result; Root string; Dir string }`.
  - `type Linker struct { Tracker Tracker; OpenCode OpenCode }`, `func (l Linker) Link(ctx context.Context, dir, id, session string) (Outcome, error)`.
  - Test fakes in package `link`: `fakeTracker`, `fakeOpenCode`, `sess`, `task`, `world` (reused by Task 3).

- [ ] **Step 1: Write the failing tracker test**

In `internal/tracker/tracker_test.go`, change the fake so a failing call still returns its stdout (bd prints JSON errors on stdout):

```go
func (f *fakeRun) run(_ context.Context, dir string, args ...string) ([]byte, error) {
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	f.dirs = append(f.dirs, dir)
	if e := f.err[k]; e != nil {
		return []byte(f.out[k]), e
	}
	return []byte(f.out[k]), nil
}
```

Append:

```go
func TestShowNotFound(t *testing.T) {
	k := "show tsk-zz --json"
	f := &fakeRun{
		out: map[string]string{k: `{"error":"no issues found matching the provided IDs","schema_version":1}`},
		err: map[string]error{k: errors.New("exit status 1")},
	}
	_, err := newBD(f).Show(context.Background(), "/r", "tsk-zz")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	f = &fakeRun{out: map[string]string{"show tsk-zz --json": `[]`}}
	if _, err := newBD(f).Show(context.Background(), "/r", "tsk-zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty array err = %v, want ErrNotFound", err)
	}
}

func TestShowOtherFailureIsNotNotFound(t *testing.T) {
	k := "show tsk-zz --json"
	f := &fakeRun{err: map[string]error{k: errors.New("no beads database found")}}
	_, err := newBD(f).Show(context.Background(), "/r", "tsk-zz")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tracker/`
Expected: FAIL, `undefined: ErrNotFound`.

- [ ] **Step 3: Implement in `internal/tracker/tracker.go`**

Add `"errors"` to the imports. Add below the `const` block:

```go
// ErrNotFound is returned (wrapped) by Show when bd has no such task.
var ErrNotFound = errors.New("not found")
```

Replace `Show`:

```go
func (b BD) Show(ctx context.Context, repo, id string) (Task, error) {
	out, err := b.Run(ctx, repo, "show", id, "--json")
	if err != nil {
		if notFound(out) {
			return Task{}, fmt.Errorf("bd show %s: %w", id, ErrNotFound)
		}
		return Task{}, err
	}
	var tasks []Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return Task{}, fmt.Errorf("bd show: unexpected output: %w", err)
	}
	if len(tasks) == 0 {
		return Task{}, fmt.Errorf("bd show %s: %w", id, ErrNotFound)
	}
	return tasks[0], nil
}

// notFound reports bd's JSON answer for IDs it does not know.
func notFound(out []byte) bool {
	var v struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(out, &v) == nil && strings.Contains(v.Error, "no issues found")
}
```

Run: `go test ./internal/tracker/`
Expected: PASS (the existing `bd show se-9: not found` assertion still holds).

- [ ] **Step 4: Write the failing link test**

`internal/link/link_test.go`:

```go
package link

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

type fakeTracker struct {
	tasks   map[string]tracker.Task
	showErr error
	shown   []string // "dir id"
	set     []string // "dir id session worktree"
}

func (f *fakeTracker) Show(_ context.Context, dir, id string) (tracker.Task, error) {
	f.shown = append(f.shown, dir+" "+id)
	if f.showErr != nil {
		return tracker.Task{}, f.showErr
	}
	t, ok := f.tasks[id]
	if !ok {
		return tracker.Task{}, fmt.Errorf("bd show %s: %w", id, tracker.ErrNotFound)
	}
	return t, nil
}

func (f *fakeTracker) SetMetadata(_ context.Context, dir, id string, kv map[string]string) error {
	f.set = append(f.set, fmt.Sprintf("%s %s %s %s", dir, id, kv[tracker.MetaSession], kv[tracker.MetaWorktree]))
	return nil
}

type fakeOpenCode struct {
	sessions map[string]opencode.Session
	err      error            // every call fails with it when set
	errFor   map[string]error // GetSession(id) fails with it when set
}

func (f *fakeOpenCode) GetSession(_ context.Context, id string) (opencode.Session, error) {
	if f.err != nil {
		return opencode.Session{}, f.err
	}
	if e := f.errFor[id]; e != nil {
		return opencode.Session{}, e
	}
	s, ok := f.sessions[id]
	if !ok {
		return opencode.Session{}, fmt.Errorf("GET /api/session/%s: %w", id, opencode.ErrNotFound)
	}
	return s, nil
}

func (f *fakeOpenCode) Root(ctx context.Context, id string) (string, error) {
	for range 10 {
		s, err := f.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		if s.ParentID == "" {
			return id, nil
		}
		id = s.ParentID
	}
	return "", errors.New("parent loop")
}

func sess(id, parent, dir string) opencode.Session {
	s := opencode.Session{ID: id, ParentID: parent}
	s.Location.Directory = dir
	return s
}

func task(id, status, session string) tracker.Task {
	t := tracker.Task{ID: id, Status: status, Metadata: map[string]any{}}
	if session != "" {
		t.Metadata[tracker.MetaSession] = session
	}
	return t
}

func world() (*fakeTracker, *fakeOpenCode, Linker) {
	tr := &fakeTracker{tasks: map[string]tracker.Task{}}
	oc := &fakeOpenCode{sessions: map[string]opencode.Session{
		"ses_root":  sess("ses_root", "", "/r/.worktrees/x"),
		"ses_kid":   sess("ses_kid", "ses_root", "/r/.worktrees/x"),
		"ses_other": sess("ses_other", "", "/r"),
	}}
	return tr, oc, Linker{Tracker: tr, OpenCode: oc}
}

func TestLink(t *testing.T) {
	tests := []struct {
		name    string
		task    *tracker.Task
		session string
		want    Result
		writes  bool
	}{
		{"links the root of a child session", ptr(task("tsk-a", "in_progress", "")), "ses_kid", Linked, true},
		{"not found", nil, "ses_root", NotFound, false},
		{"not in progress", ptr(task("tsk-a", "open", "")), "ses_root", NotInProgress, false},
		{"same session", ptr(task("tsk-a", "in_progress", "ses_root")), "ses_kid", Same, false},
		{"live other session", ptr(task("tsk-a", "in_progress", "ses_other")), "ses_root", LiveSession, false},
		{"gone session replaced", ptr(task("tsk-a", "in_progress", "ses_gone")), "ses_root", Linked, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, l := world()
			if tt.task != nil {
				tr.tasks["tsk-a"] = *tt.task
			}
			out, err := l.Link(context.Background(), "/r", "tsk-a", tt.session)
			if err != nil {
				t.Fatal(err)
			}
			if out.Result != tt.want || out.Root != "ses_root" || out.Dir != "/r/.worktrees/x" {
				t.Fatalf("outcome = %+v, want %s with root ses_root", out, tt.want)
			}
			var want []string
			if tt.writes {
				want = []string{"/r tsk-a ses_root /r/.worktrees/x"}
			}
			if !slices.Equal(tr.set, want) {
				t.Fatalf("set = %v, want %v", tr.set, want)
			}
		})
	}
}

func TestLinkErrors(t *testing.T) {
	ctx := context.Background()

	tr, oc, l := world()
	tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "")
	oc.err = errors.New("connection refused")
	if _, err := l.Link(ctx, "/r", "tsk-a", "ses_root"); err == nil || len(tr.set) != 0 {
		t.Fatalf("unreachable OpenCode: err = %v, set = %v", err, tr.set)
	}

	tr, _, l = world()
	tr.showErr = errors.New("no beads database found")
	if _, err := l.Link(ctx, "/r", "tsk-a", "ses_root"); err == nil || len(tr.set) != 0 {
		t.Fatalf("bd failure: err = %v, set = %v", err, tr.set)
	}

	tr, oc, l = world()
	tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "ses_flaky")
	oc.errFor = map[string]error{"ses_flaky": errors.New("HTTP 500")}
	if _, err := l.Link(ctx, "/r", "tsk-a", "ses_root"); err == nil || len(tr.set) != 0 {
		t.Fatalf("old session check failure: err = %v, set = %v", err, tr.set)
	}
}

func ptr[T any](v T) *T { return &v }
```

- [ ] **Step 5: Run to verify it fails**

Run: `go test ./internal/link/`
Expected: FAIL, `undefined: Linker`, `undefined: Result`.

- [ ] **Step 6: Implement `internal/link/link.go`**

```go
package link

import (
	"context"
	"errors"
	"fmt"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Tracker is the part of the tracker link uses.
type Tracker interface {
	Show(ctx context.Context, repo, id string) (tracker.Task, error)
	SetMetadata(ctx context.Context, repo, id string, kv map[string]string) error
}

// OpenCode is the part of the OpenCode client link uses.
type OpenCode interface {
	Root(ctx context.Context, id string) (string, error)
	GetSession(ctx context.Context, id string) (opencode.Session, error)
}

// Result is what Link decided for a task.
type Result string

const (
	Linked        Result = "linked"
	Same          Result = "same"
	NotFound      Result = "not_found"
	NotInProgress Result = "not_in_progress"
	LiveSession   Result = "live_session"
)

// Outcome is Link's decision, with the root session and its directory.
type Outcome struct {
	Result Result
	Root   string
	Dir    string
}

// Linker links tasks to OpenCode sessions.
type Linker struct {
	Tracker  Tracker
	OpenCode OpenCode
}

// Link links task id, read with bd in dir, to the root of session. Only an
// in-progress task is linked, and a session OpenCode still has is never
// replaced.
func (l Linker) Link(ctx context.Context, dir, id, session string) (Outcome, error) {
	root, err := l.OpenCode.Root(ctx, session)
	if err != nil {
		return Outcome{}, fmt.Errorf("resolve session %s: %w", session, err)
	}
	rs, err := l.OpenCode.GetSession(ctx, root)
	if err != nil {
		return Outcome{}, fmt.Errorf("session %s: %w", root, err)
	}
	out := Outcome{Root: root, Dir: rs.Location.Directory}

	t, err := l.Tracker.Show(ctx, dir, id)
	if errors.Is(err, tracker.ErrNotFound) {
		out.Result = NotFound
		return out, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if t.Status != "in_progress" {
		out.Result = NotInProgress
		return out, nil
	}
	switch cur := t.Meta(tracker.MetaSession); cur {
	case root:
		out.Result = Same
		return out, nil
	case "":
	default:
		_, err := l.OpenCode.GetSession(ctx, cur)
		if err == nil {
			out.Result = LiveSession
			return out, nil
		}
		if !errors.Is(err, opencode.ErrNotFound) {
			return Outcome{}, fmt.Errorf("session %s: %w", cur, err)
		}
	}

	meta := map[string]string{tracker.MetaSession: root}
	if out.Dir != "" {
		meta[tracker.MetaWorktree] = out.Dir
	}
	if err := l.Tracker.SetMetadata(ctx, dir, id, meta); err != nil {
		return Outcome{}, err
	}
	out.Result = Linked
	return out, nil
}
```

- [ ] **Step 7: Run tests**

Run: `make test && make lint`
Expected: PASS, lint clean.

- [ ] **Step 8: Commit**

```bash
git add internal/tracker internal/link/link.go internal/link/link_test.go
git commit -m "feat: link rule for tasks claimed outside dispatch

Task: tsk-jqp"
```

---

### Task 3: `tsk link` command and its log

**Files:**
- Create: `internal/link/command.go`
- Test: `internal/link/command_test.go`
- Modify: `cmd/tsk/main.go`

**Interfaces:**
- Consumes: `ParseClaims`, `resolve` (Task 1); `Linker`, `Outcome`, `Tracker`, `OpenCode`, test fakes `world`, `task`, `sess` (Task 2); `tracker.New()`, `opencode.FromService(ctx) (*opencode.Client, error)`.
- Produces:
  - `type Entry struct { Dir, ID string; Outcome Outcome; Err error }`
  - `func (l Linker) Claims(ctx context.Context, session string, claims []Claim) []Entry`
  - `func LogPath() (string, error)`, `func WriteLog(w io.Writer, at time.Time, session string, entries []Entry) error`
  - `type Deps struct { Tracker Tracker; Connect func(context.Context) (OpenCode, error); Log func() (io.WriteCloser, error); Now func() time.Time }`, `func Default() Deps`
  - `func Main(ctx context.Context, args []string, stderr io.Writer, d Deps) int` (0 ok, 1 any error, 2 bad arguments)

- [ ] **Step 1: Write the failing test**

`internal/link/command_test.go`:

```go
package link

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

var at = time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)

type cliHarness struct {
	tr       *fakeTracker
	oc       *fakeOpenCode
	log      bytes.Buffer
	stderr   bytes.Buffer
	connects int
	deps     Deps
}

func newCLI() *cliHarness {
	tr, oc, _ := world()
	h := &cliHarness{tr: tr, oc: oc}
	h.deps = Deps{
		Tracker: tr,
		Connect: func(context.Context) (OpenCode, error) { h.connects++; return oc, nil },
		Log:     func() (io.WriteCloser, error) { return nopWriteCloser{&h.log}, nil },
		Now:     func() time.Time { return at },
	}
	return h
}

func (h *cliHarness) run(args ...string) int {
	return Main(context.Background(), args, &h.stderr, h.deps)
}

func TestMainBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--command", "bd update tsk-a --claim"},
		{"--session", "ses_kid"},
		{"--session", "ses_kid", "--command", "x", "extra"},
		{"--bogus"},
	} {
		h := newCLI()
		if code := h.run(args...); code != 2 {
			t.Fatalf("%v: code = %d, want 2", args, code)
		}
	}
}

func TestMainNoClaimSkipsOpenCode(t *testing.T) {
	h := newCLI()
	if code := h.run("--session", "ses_kid", "--command", "go test ./..."); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if h.connects != 0 || h.log.Len() != 0 {
		t.Fatalf("connects = %d, log = %q", h.connects, h.log.String())
	}
}

func TestMainLinksInSessionDirectory(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "")
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a --claim"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, h.stderr.String())
	}
	if !slices.Equal(h.tr.set, []string{"/r/.worktrees/x tsk-a ses_root /r/.worktrees/x"}) {
		t.Fatalf("set = %v", h.tr.set)
	}
	want := "2026-10-09T18:00:00Z ses_kid /r/.worktrees/x tsk-a linked ses_root\n"
	if h.log.String() != want {
		t.Fatalf("log = %q, want %q", h.log.String(), want)
	}
}

func TestMainWorkdir(t *testing.T) {
	for workdir, dir := range map[string]string{"sub": "/r/.worktrees/x/sub", "/elsewhere": "/elsewhere"} {
		h := newCLI()
		h.run("--session", "ses_kid", "--workdir", workdir, "--command", "bd update tsk-a --claim")
		if !slices.Equal(h.tr.shown, []string{dir + " tsk-a"}) {
			t.Fatalf("workdir %s: shown = %v, want %s", workdir, h.tr.shown, dir)
		}
	}
}

func TestMainConnectFails(t *testing.T) {
	h := newCLI()
	h.deps.Connect = func(context.Context) (OpenCode, error) { return nil, errors.New("connection refused") }
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a tsk-b --claim"); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(h.log.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "tsk-a error: connection refused") || !strings.HasSuffix(lines[1], "tsk-b error: connection refused") {
		t.Fatalf("log = %q", h.log.String())
	}
	if len(h.tr.set) != 0 {
		t.Fatalf("set = %v", h.tr.set)
	}
}

func TestMainResultsAreLogged(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "open", "")
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a fix-this --claim"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	got := h.log.String()
	if !strings.Contains(got, " tsk-a not_in_progress\n") || !strings.Contains(got, " fix-this not_found\n") {
		t.Fatalf("log = %q", got)
	}
}

func TestMainLogUnavailableFallsBackToStderr(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "")
	h.deps.Log = func() (io.WriteCloser, error) { return nil, errors.New("read-only") }
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a --claim"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(h.stderr.String(), "read-only") || !strings.Contains(h.stderr.String(), "tsk-a linked ses_root") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

func TestLogPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if p, err := LogPath(); err != nil || p != "/state/tsk/link.log" {
		t.Fatalf("p = %q, err = %v", p, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if p, err := LogPath(); err != nil || p != "/home/u/.local/state/tsk/link.log" {
		t.Fatalf("p = %q, err = %v", p, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/link/`
Expected: FAIL, `undefined: Deps`, `undefined: Main`, `undefined: LogPath`.

- [ ] **Step 3: Implement `internal/link/command.go`**

```go
package link

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Entry is the outcome of linking one claimed task.
type Entry struct {
	Dir     string
	ID      string
	Outcome Outcome
	Err     error
}

// Claims links every task in claims to session.
func (l Linker) Claims(ctx context.Context, session string, claims []Claim) []Entry {
	var entries []Entry
	for _, c := range claims {
		for _, id := range c.IDs {
			out, err := l.Link(ctx, c.Dir, id, session)
			entries = append(entries, Entry{Dir: c.Dir, ID: id, Outcome: out, Err: err})
		}
	}
	return entries
}

// LogPath is where tsk link records its decisions.
func LogPath() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "tsk", "link.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tsk", "link.log"), nil
}

// WriteLog writes one line per entry: time, session, directory, task, and
// the result (with the root session when linked) or the error.
func WriteLog(w io.Writer, at time.Time, session string, entries []Entry) error {
	for _, e := range entries {
		what := string(e.Outcome.Result)
		switch {
		case e.Err != nil:
			what = "error: " + strings.ReplaceAll(e.Err.Error(), "\n", "; ")
		case e.Outcome.Result == Linked || e.Outcome.Result == Same:
			what += " " + e.Outcome.Root
		}
		if _, err := fmt.Fprintf(w, "%s %s %s %s %s\n", at.UTC().Format(time.RFC3339), session, e.Dir, e.ID, what); err != nil {
			return err
		}
	}
	return nil
}

// Deps are what tsk link uses from the outside world.
type Deps struct {
	Tracker Tracker
	Connect func(ctx context.Context) (OpenCode, error)
	Log     func() (io.WriteCloser, error)
	Now     func() time.Time
}

// Default returns the real dependencies.
func Default() Deps {
	return Deps{
		Tracker: tracker.New(),
		Connect: func(ctx context.Context) (OpenCode, error) {
			c, err := opencode.FromService(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Log: openLog,
		Now: time.Now,
	}
}

func openLog() (io.WriteCloser, error) {
	p, err := LogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

const (
	usage      = "usage: tsk link --session <id> [--workdir <dir>] --command <command>"
	runTimeout = time.Minute
)

// Main runs tsk link with the arguments after "link". It returns 0 when every
// claimed task got a result, 1 when any got an error, and 2 on bad arguments.
func Main(ctx context.Context, args []string, stderr io.Writer, d Deps) int {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "OpenCode session that ran the command")
	workdir := fs.String("workdir", "", "workdir of the shell call, if it had one")
	command := fs.String("command", "", "the shell command")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" || *command == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	// Most commands claim nothing; those never contact OpenCode.
	if len(ParseClaims("/", *command)) == 0 {
		return 0
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	entries := run(ctx, d, *session, *workdir, *command)
	code := 0
	for _, e := range entries {
		if e.Err != nil {
			code = 1
		}
	}
	w, err := d.Log()
	if err != nil {
		fmt.Fprintf(stderr, "tsk link: open log: %v\n", err)
		w = nopCloser{stderr}
	}
	defer w.Close()
	if err := WriteLog(w, d.Now(), *session, entries); err != nil {
		fmt.Fprintf(stderr, "tsk link: write log: %v\n", err)
	}
	return code
}

// run links the claims in command. The base directory is the session's own;
// workdir, when set, replaces it (absolute) or is joined to it (relative).
func run(ctx context.Context, d Deps, session, workdir, command string) []Entry {
	fail := func(err error) []Entry {
		base := "-"
		if filepath.IsAbs(workdir) {
			base = workdir
		}
		var entries []Entry
		for _, c := range ParseClaims(base, command) {
			for _, id := range c.IDs {
				entries = append(entries, Entry{Dir: c.Dir, ID: id, Err: err})
			}
		}
		return entries
	}
	oc, err := d.Connect(ctx)
	if err != nil {
		return fail(err)
	}
	s, err := oc.GetSession(ctx, session)
	if err != nil {
		return fail(fmt.Errorf("session %s: %w", session, err))
	}
	dir := s.Location.Directory
	if workdir != "" {
		dir = resolve(dir, workdir)
	}
	l := Linker{Tracker: d.Tracker, OpenCode: oc}
	return l.Claims(ctx, session, ParseClaims(dir, command))
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/link/`
Expected: PASS.

- [ ] **Step 5: Wire the subcommand in `cmd/tsk/main.go`**

Add `"github.com/AntoineGS/taskestrator/internal/link"` to the imports and replace everything after the imports with:

```go
const usage = `tsk is a bt plugin; bt starts it. Add it to ~/.config/bt/config.yaml:

  plugins:
    - name: tsk
      command: ["tsk", "plugin"]
      options:
        roots: ["~/gits"]
        model: "anthropic/claude-opus-5-5"   # optional

usage:
  tsk plugin    speak the bt plugin protocol on stdin/stdout (started by bt)
  tsk link      link the tasks a shell command claimed to its OpenCode session
                (run by the tsk OpenCode plugin; see the README)`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "link" {
		code := link.Main(ctx, args[1:], os.Stderr, link.Default())
		cancel()
		os.Exit(code)
	}
	err := run(ctx, args)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsk:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "plugin" {
		return errors.New(usage)
	}
	return btplugin.Main(ctx, os.Stdin, os.Stdout)
}
```

- [ ] **Step 6: Smoke-test the binary**

Run: `go build -o bin/tsk ./cmd/tsk && ./bin/tsk link; echo "exit=$?"; ./bin/tsk link --session ses_x --command "go test ./..."; echo "exit=$?"`
Expected: the first prints the usage line and `exit=2`; the second prints nothing and `exit=0`.

- [ ] **Step 7: Full checks and commit**

Run: `make test && make lint`
Expected: PASS, lint clean.

```bash
git add internal/link/command.go internal/link/command_test.go cmd/tsk/main.go
git commit -m "feat: tsk link links claimed tasks to the claiming session

Task: tsk-jqp"
```

---

### Task 4: OpenCode plugin

**Files:**
- Create: `opencode/tsk-link.js`
- Test: `opencode/tsk-link.test.mjs`
- Modify: `Makefile` (`test` target)

**Interfaces:**
- Consumes: the `tsk link` CLI from Task 3 (`--session`, optional `--workdir`, `--command`).
- Produces: default export `{ id: "tsk.link", setup(ctx) }`; named exports `looksLikeClaim(command) → boolean`, `linkArgs(event) → string[] | undefined`, `start(args, run = spawn) → void`.

- [ ] **Step 1: Write the failing test**

`opencode/tsk-link.test.mjs`:

```js
import test from "node:test"
import assert from "node:assert/strict"
import plugin, { looksLikeClaim, linkArgs, start } from "./tsk-link.js"

const shell = (command, extra = {}) => ({
  tool: "shell",
  status: "completed",
  sessionID: "ses_a",
  input: { command, ...extra },
})

test("looksLikeClaim", () => {
  assert.equal(looksLikeClaim("bd update tsk-a --claim"), true)
  assert.equal(looksLikeClaim("bd update tsk-a -s in_progress"), true)
  assert.equal(looksLikeClaim("bd show tsk-a"), false)
  assert.equal(looksLikeClaim("bd update tsk-a --notes x"), false)
  assert.equal(looksLikeClaim("go test ./..."), false)
  assert.equal(looksLikeClaim(undefined), false)
})

test("linkArgs for a claim", () => {
  assert.deepEqual(linkArgs(shell("bd update tsk-a --claim")), [
    "link", "--session", "ses_a", "--command", "bd update tsk-a --claim",
  ])
  assert.deepEqual(linkArgs(shell("bd update tsk-a --claim", { workdir: "sub" })), [
    "link", "--session", "ses_a", "--workdir", "sub", "--command", "bd update tsk-a --claim",
  ])
})

test("linkArgs ignores other calls", () => {
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), tool: "read" }), undefined)
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), status: "error" }), undefined)
  assert.equal(linkArgs(shell("ls")), undefined)
  assert.equal(linkArgs({ ...shell("bd update tsk-a --claim"), sessionID: undefined }), undefined)
  assert.equal(linkArgs(undefined), undefined)
})

test("start spawns tsk detached and never throws", () => {
  const calls = []
  const child = { on: (name) => calls.push(["on", name]), unref: () => calls.push(["unref"]) }
  start(["link"], (cmd, args, opts) => {
    calls.push([cmd, args, opts])
    return child
  })
  assert.deepEqual(calls[0], ["tsk", ["link"], { detached: true, stdio: "ignore" }])
  assert.deepEqual(calls.slice(1), [["on", "error"], ["unref"]])
  assert.doesNotThrow(() => start(["link"], () => { throw new Error("ENOENT") }))
})

test("setup registers execute.after and ignores non-claims", async () => {
  const hooks = []
  await plugin.setup({ tool: { hook: async (name, cb) => hooks.push([name, cb]) } })
  assert.equal(plugin.id, "tsk.link")
  assert.equal(hooks.length, 1)
  assert.equal(hooks[0][0], "execute.after")
  assert.doesNotThrow(() => hooks[0][1](shell("ls")))
  assert.doesNotThrow(() => hooks[0][1](null))
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `node --test opencode/tsk-link.test.mjs`
Expected: FAIL, cannot find module `./tsk-link.js`.

- [ ] **Step 3: Implement `opencode/tsk-link.js`**

```js
// tsk OpenCode plugin: when an agent claims a Beads task from a shell
// command, `tsk link` links the task to the agent's session so bt can show
// its live state. Install by symlinking this file into
// ~/.config/opencode/plugins/. It never fails or delays a tool call.

import { spawn } from "node:child_process"

// A cheap pre-filter; tsk link does the real parsing.
export function looksLikeClaim(command) {
  return (
    typeof command === "string" &&
    /\bbd\b/.test(command) &&
    /\bupdate\b/.test(command) &&
    /--claim|in_progress/.test(command)
  )
}

// The tsk arguments for a finished tool call, or undefined to do nothing.
export function linkArgs(event) {
  if (event?.tool !== "shell" || event.status !== "completed") return undefined
  if (typeof event.sessionID !== "string" || event.sessionID === "") return undefined
  const command = event.input?.command
  if (!looksLikeClaim(command)) return undefined
  const args = ["link", "--session", event.sessionID]
  const workdir = event.input?.workdir
  if (typeof workdir === "string" && workdir !== "") args.push("--workdir", workdir)
  args.push("--command", command)
  return args
}

export function start(args, run = spawn) {
  try {
    const child = run("tsk", args, { detached: true, stdio: "ignore" })
    child.on("error", () => {})
    child.unref()
  } catch {
    // tsk missing or not startable: linking is best effort.
  }
}

export default {
  id: "tsk.link",
  async setup(ctx) {
    await ctx.tool.hook("execute.after", (event) => {
      try {
        const args = linkArgs(event)
        if (args) start(args)
      } catch {
        // Never let linking affect the tool call.
      }
    })
  },
}
```

- [ ] **Step 4: Run tests**

Run: `node --test opencode/tsk-link.test.mjs`
Expected: PASS, 5 tests.

- [ ] **Step 5: Add the JS test to `make test`**

In `Makefile`, change the `test` target to:

```make
test:
	go test ./...
	node --test opencode/*.test.mjs
```

Run: `make test && make lint`
Expected: Go tests PASS, then node reports 5 passing tests.

- [ ] **Step 6: Commit**

```bash
git add opencode/tsk-link.js opencode/tsk-link.test.mjs Makefile
git commit -m "feat: OpenCode plugin runs tsk link after claiming shell calls

Task: tsk-jqp"
```

---

### Task 5: `herdr.Agents` and `herdr.Within`

**Files:**
- Modify: `internal/herdr/herdr.go`
- Test: `internal/herdr/herdr_test.go`

**Interfaces:**
- Produces:
  - `type Agent struct { PaneID, Cwd, SessionID, Source, Status string }`
  - `type AgentLister interface { Agents(ctx context.Context) ([]Agent, error) }`; `CLI` implements it.
  - `func Within(path, root string) bool` (replaces unexported `within`; cleans both arguments).

- [ ] **Step 1: Write the failing test**

Append to `internal/herdr/herdr_test.go`:

```go
func TestAgents(t *testing.T) {
	f := &fakeRun{out: map[string]string{"agent list": `{"id":"cli:agent:list","result":{"agents":[
{"agent":"opencode","agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode","value":"ses_a"},"agent_status":"idle","cwd":"/src/repo","pane_id":"w1:p1"},
{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"c-1"},"agent_status":"working","cwd":"/src/repo","pane_id":"w1:p2"},
{"agent":"opencode","agent_status":"idle","cwd":"/src/repo","pane_id":"w1:p3"}]}}`}}
	got, err := CLI{Exec: f.run}.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{PaneID: "w1:p1", Cwd: "/src/repo", SessionID: "ses_a", Source: "herdr:opencode", Status: "idle"},
		{PaneID: "w1:p2", Cwd: "/src/repo", SessionID: "c-1", Source: "herdr:claude", Status: "working"},
		{PaneID: "w1:p3", Cwd: "/src/repo", Status: "idle"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("agents = %+v, want %+v", got, want)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		path, root string
		want       bool
	}{
		{"/src/repo", "/src/repo", true},
		{"/src/repo/.worktrees/x", "/src/repo", true},
		{"/src/repo/", "/src/repo", true},
		{"/src/repository", "/src/repo", false},
		{"/src", "/src/repo", false},
	} {
		if got := Within(c.path, c.root); got != c.want {
			t.Errorf("Within(%q, %q) = %v", c.path, c.root, got)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/herdr/`
Expected: FAIL, `undefined: Agent`, `undefined: Within`.

- [ ] **Step 3: Implement in `internal/herdr/herdr.go`**

Replace `within` with:

```go
// Within reports whether path is root or inside it.
func Within(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
```

In `WorkspaceFor`, change `case within(p.Cwd, repo):` to `case Within(p.Cwd, repo):`.

Append:

```go
// Agent is an agent pane as herdr reports it.
type Agent struct {
	PaneID    string
	Cwd       string
	SessionID string // "" when herdr has no session for the pane
	Source    string // who reported the session, such as "herdr:opencode"
	Status    string // herdr's agent_status: idle, working, blocked or done
}

// AgentLister lists agent panes.
type AgentLister interface {
	Agents(ctx context.Context) ([]Agent, error)
}

func (h CLI) Agents(ctx context.Context) ([]Agent, error) {
	var res struct {
		Agents []struct {
			PaneID  string `json:"pane_id"`
			Cwd     string `json:"cwd"`
			Status  string `json:"agent_status"`
			Session *struct {
				Value  string `json:"value"`
				Source string `json:"source"`
			} `json:"agent_session"`
		} `json:"agents"`
	}
	if err := h.call(ctx, &res, "agent", "list"); err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(res.Agents))
	for _, a := range res.Agents {
		ag := Agent{PaneID: a.PaneID, Cwd: a.Cwd, Status: a.Status}
		if a.Session != nil {
			ag.SessionID, ag.Source = a.Session.Value, a.Session.Source
		}
		out = append(out, ag)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

Run: `make test && make lint`
Expected: PASS, lint clean.

- [ ] **Step 5: Commit**

```bash
git add internal/herdr
git commit -m "feat: herdr lists agent panes with their sessions

Task: tsk-jqp"
```

---

### Task 6: `untracked` state and the `link` action offer

**Files:**
- Modify: `internal/btplugin/engine.go` (`state`)
- Modify: `internal/btplugin/protocol.go` (`Manifest`)
- Test: `internal/btplugin/engine_test.go`, `internal/btplugin/protocol_test.go` (create it if absent)

**Interfaces:**
- Consumes: `status.Untracked`, `status.Lost`.
- Produces: pushes `tsk.state = untracked` with actions `[link]` for in-progress beads without a session; lost beads get `[dispatch, link]`; manifest value `untracked` and action `{ID: "link", Label: "Link agent session", Key: "K"}`. Task 7 implements the action.

- [ ] **Step 1: Update the mapping test (failing)**

In `TestMapping` in `internal/btplugin/engine_test.go`, replace the `untracked`, `lost`, `lost blocked` and `lost no herdr` rows with:

```go
		{"untracked", mkBead("a", "in_progress", ""), nil, false, envOK(), "untracked", []string{"link"}},
		{"untracked no herdr", mkBead("a", "in_progress", ""), nil, false, Env{RepoFor: envOK().RepoFor}, "untracked", nil},
		{"untracked no repo", mkBead("a", "in_progress", ""), nil, false, Env{HerdrUp: true, RepoFor: func(Bead) string { return "" }}, "untracked", nil},
		{"untracked blocked bead", blocked(mkBead("a", "in_progress", "")), nil, false, envOK(), "untracked", []string{"link"}},
		{"untracked blocked status", mkBead("a", "blocked", ""), nil, false, envOK(), "", nil},
		{"lost", mkBead("a", "in_progress", "s"), &status.Snapshot{}, false, envOK(), "lost", []string{"dispatch", "link"}},
		{"lost blocked", blocked(mkBead("a", "in_progress", "s")), &status.Snapshot{}, false, envOK(), "lost", []string{"link"}},
		{"lost no herdr", mkBead("a", "in_progress", "s"), &status.Snapshot{}, false, Env{RepoFor: envOK().RepoFor}, "lost", nil},
```

Append to `engine_test.go`:

```go
func TestUntrackedHasNoSection(t *testing.T) {
	e, _ := newEng()
	set, _, _ := e.Sync([]Bead{mkBead("a", "in_progress", "")}, envOK())
	if st := one(t, set); st.Sections != nil {
		t.Fatalf("sections = %v", st.Sections)
	}
}
```

Add a manifest test to `internal/btplugin/protocol_test.go` (if the file does not exist, create it with `package btplugin` and `import "testing"`):

```go
func TestManifestDeclaresLink(t *testing.T) {
	m := Manifest()
	var untracked *FieldValue
	for i, v := range m.Fields[0].Values {
		if v.ID == "untracked" {
			untracked = &m.Fields[0].Values[i]
		}
	}
	if untracked == nil || untracked.Label != "Not linked" || untracked.Badge != "" || untracked.Lane == nil || *untracked.Lane {
		t.Fatalf("untracked value = %+v", untracked)
	}
	found := false
	for _, a := range m.Actions {
		if a == (ActionDecl{ID: "link", Label: "Link agent session", Key: "K"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("actions = %+v", m.Actions)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/btplugin/ -run 'TestMapping|TestUntrackedHasNoSection|TestManifestDeclaresLink'`
Expected: FAIL (untracked not pushed; no link action; the manifest lacks the value).

- [ ] **Step 3: Implement**

In `internal/btplugin/engine.go` `state`, replace the `dispatch` helper and add `link`:

```go
	dispatch := func() {
		if !b.Blocked && env.HerdrUp && env.RepoFor != nil && env.RepoFor(b) != "" {
			actions = append(actions, "dispatch")
		}
	}
	link := func() {
		if env.HerdrUp && env.RepoFor != nil && env.RepoFor(b) != "" {
			actions = append(actions, "link")
		}
	}
```

Change the `Lost` case to:

```go
	case status.Lost:
		value = "lost"
		dispatch()
		link()
```

and add before `default`:

```go
	case status.Untracked:
		if b.Status != "in_progress" {
			delete(e.since, k)
			return BeadState{}, false
		}
		value = "untracked"
		link()
```

In `internal/btplugin/protocol.go` `Manifest`, add after the `queued` value:

```go
				{ID: "untracked", Label: "Not linked", Lane: &no},
```

and after the `jump` action:

```go
			{ID: "link", Label: "Link agent session", Key: "K"},
```

- [ ] **Step 4: Run tests**

Run: `make test`
Expected: PASS. If another existing test asserted that an in-progress bead without a session pushes nothing, or that a lost bead's actions are exactly `[dispatch]`, update it to the mapping above and say so in the commit body.

Run: `make lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/btplugin
git commit -m "feat: show untracked in-progress tasks and offer linking

Task: tsk-jqp"
```

---

### Task 7: The `link` action

**Files:**
- Modify: `internal/btplugin/protocol.go` (`SelectParams`, `SelectOption`)
- Modify: `internal/btplugin/actions.go` (fields, `Invoke`, `acquire`, `linkSession`, `refusal`, `sessionOptions`)
- Modify: `internal/btplugin/server.go` (`choose`, `Main` wiring, `newLinker`)
- Modify: `internal/btplugin/fakes_test.go` (recorder `Agents`, `linkWorld`, `ocSession`)
- Test: `internal/btplugin/actions_test.go`, `internal/btplugin/rpc_test.go`

**Interfaces:**
- Consumes: `link.Linker`, `link.Linked`, `link.Same`, `link.NotFound`, `link.NotInProgress`, `link.LiveSession` (Task 2); `herdr.Agent`, `herdr.AgentLister`, `herdr.Within` (Task 5); `status.Untracked`, `status.Lost`.
- Produces: `Actions.Agents herdr.AgentLister`, `Actions.Linker func() (link.Linker, error)`, `Actions.Select func(ctx, SelectParams) (string, error)`; `Server.choose`; action `link`; error types `no_sessions`, `not_linkable`.

- [ ] **Step 1: Add the test fakes**

In `internal/btplugin/fakes_test.go`, add the import `"github.com/AntoineGS/taskestrator/internal/herdr"`, add a field to `recorder`:

```go
	agents []herdr.Agent // returned by Agents
```

and append:

```go
func (r *recorder) Agents(context.Context) ([]herdr.Agent, error) {
	return r.agents, r.note("agents")
}

// linkWorld fakes what linking reads and writes: OpenCode sessions and bd
// tasks.
type linkWorld struct {
	sessions map[string]opencode.Session
	tasks    map[string]tracker.Task
	set      []string // "dir id session worktree"
}

func (w *linkWorld) GetSession(_ context.Context, id string) (opencode.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return opencode.Session{}, fmt.Errorf("GET /api/session/%s: %w", id, opencode.ErrNotFound)
	}
	return s, nil
}

func (w *linkWorld) Root(ctx context.Context, id string) (string, error) {
	s, err := w.GetSession(ctx, id)
	if err != nil {
		return "", err
	}
	if s.ParentID != "" {
		return w.Root(ctx, s.ParentID)
	}
	return id, nil
}

func (w *linkWorld) Show(_ context.Context, _, id string) (tracker.Task, error) {
	t, ok := w.tasks[id]
	if !ok {
		return tracker.Task{}, fmt.Errorf("bd show %s: %w", id, tracker.ErrNotFound)
	}
	return t, nil
}

func (w *linkWorld) SetMetadata(_ context.Context, dir, id string, kv map[string]string) error {
	w.set = append(w.set, fmt.Sprintf("%s %s %s %s", dir, id, kv[tracker.MetaSession], kv[tracker.MetaWorktree]))
	return nil
}

func ocSession(id, dir, title string) opencode.Session {
	s := opencode.Session{ID: id, Title: title}
	s.Location.Directory = dir
	return s
}
```

- [ ] **Step 2: Write the failing action tests**

Append to `internal/btplugin/actions_test.go` (add imports `"github.com/AntoineGS/taskestrator/internal/herdr"`, `"github.com/AntoineGS/taskestrator/internal/link"`, `"github.com/AntoineGS/taskestrator/internal/tracker"`):

```go
func newLinkActions(t *testing.T, beads ...Bead) (*actionsHarness, *linkWorld, *[]SelectParams) {
	t.Helper()
	h := newActions(t, "/repo", beads...)
	w := &linkWorld{
		sessions: map[string]opencode.Session{
			"ses_a":   ocSession("ses_a", "/repo", "Fix the parser"),
			"ses_b":   ocSession("ses_b", "/repo/.worktrees/x", "Other work"),
			"ses_far": ocSession("ses_far", "/repository", "Elsewhere"),
		},
		tasks: map[string]tracker.Task{"a": {ID: "a", Status: "in_progress", Metadata: map[string]any{}}},
	}
	h.r.agents = []herdr.Agent{
		{PaneID: "w1:p1", Cwd: "/repo", SessionID: "ses_a", Source: "herdr:opencode", Status: "idle"},
		{PaneID: "w1:p2", Cwd: "/repo/.worktrees/x", SessionID: "ses_b", Source: "herdr:opencode", Status: "working"},
		{PaneID: "w1:p5", Cwd: "/repo", SessionID: "ses_a", Source: "herdr:opencode", Status: "idle"},
		{PaneID: "w2:p1", Cwd: "/repository", SessionID: "ses_far", Source: "herdr:opencode", Status: "idle"},
		{PaneID: "w1:p3", Cwd: "/repo", SessionID: "c-1", Source: "herdr:claude", Status: "idle"},
		{PaneID: "w1:p4", Cwd: "/repo", Status: "idle"},
		{PaneID: "w1:p6", Cwd: "/repo", SessionID: "ses_gone", Source: "herdr:opencode", Status: "idle"},
	}
	h.a.Agents = h.r
	h.a.Linker = func() (link.Linker, error) { return link.Linker{Tracker: w, OpenCode: w}, nil }
	var asked []SelectParams
	h.a.Select = func(_ context.Context, p SelectParams) (string, error) {
		asked = append(asked, p)
		return "ses_a", nil
	}
	return h, w, &asked
}

func TestLinkHappy(t *testing.T) {
	h, w, asked := newLinkActions(t, mkBead("a", "in_progress", ""))
	res, err := invoke(h, "link", "a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Toast == nil || res.Toast.Tone != "ok" || res.Toast.Message != "Linked a to Fix the parser" {
		t.Fatalf("toast = %+v", res.Toast)
	}
	want := []SelectOption{
		{Value: "ses_a", Label: "Fix the parser", Description: "., idle"},
		{Value: "ses_b", Label: "Other work", Description: ".worktrees/x, working"},
	}
	if len(*asked) != 1 || (*asked)[0].Title != "Link a to an agent session" || !slices.Equal((*asked)[0].Options, want) {
		t.Fatalf("asked = %+v", *asked)
	}
	if !slices.Equal(w.set, []string{"/repo a ses_a /repo"}) {
		t.Fatalf("set = %v", w.set)
	}
	if !slices.Equal(h.dispatched, []string{"ses_a"}) {
		t.Fatalf("refreshed = %v", h.dispatched)
	}
}

func TestLinkLostBead(t *testing.T) {
	h, w, _ := newLinkActions(t, mkBead("a", "in_progress", "s"))
	h.a.Engine.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: false}}, true, envOK())
	w.tasks["a"] = tracker.Task{ID: "a", Status: "in_progress", Metadata: map[string]any{tracker.MetaSession: "s"}}
	if _, err := invoke(h, "link", "a"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.set, []string{"/repo a ses_a /repo"}) {
		t.Fatalf("set = %v", w.set)
	}
}

func TestLinkDismissedOrPromptFails(t *testing.T) {
	for name, sel := range map[string]func(context.Context, SelectParams) (string, error){
		"dismissed":    func(context.Context, SelectParams) (string, error) { return "", nil },
		"prompt error": func(context.Context, SelectParams) (string, error) { return "", errors.New("no_action") },
	} {
		t.Run(name, func(t *testing.T) {
			h, w, _ := newLinkActions(t, mkBead("a", "in_progress", ""))
			h.a.Select = sel
			res, err := invoke(h, "link", "a")
			if err != nil || res.Toast != nil || len(w.set) != 0 || len(h.dispatched) != 0 {
				t.Fatalf("res = %+v, err = %v, set = %v", res, err, w.set)
			}
		})
	}
}

func TestLinkNoSessions(t *testing.T) {
	h, _, asked := newLinkActions(t, mkBead("a", "in_progress", ""))
	h.r.agents = h.r.agents[3:] // sibling directory, claude, no session, and a gone session
	_, err := invoke(h, "link", "a")
	re := wantFail(t, err, "no_sessions")
	if !strings.Contains(re.Message, "no OpenCode pane in /repo") {
		t.Fatalf("message = %q", re.Message)
	}
	if len(*asked) != 0 {
		t.Fatal("must not prompt without options")
	}
}

func TestLinkRefusals(t *testing.T) {
	tests := []struct {
		name  string
		bead  Bead
		tweak func(h *actionsHarness)
		typ   string
	}{
		{"open", mkBead("a", "open", ""), nil, "not_linkable"},
		{"running", mkBead("a", "in_progress", "s"), func(h *actionsHarness) {
			h.a.Engine.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: true, Running: true}}, true, envOK())
		}, "not_linkable"},
		{"no repo", mkBead("a", "in_progress", ""), func(h *actionsHarness) { h.a.RepoFor = func(Bead) string { return "" } }, "no_repo"},
		{"no herdr", mkBead("a", "in_progress", ""), func(h *actionsHarness) { h.a.HerdrUp = func() bool { return false } }, "no_herdr"},
		{"opencode down", mkBead("a", "in_progress", ""), func(h *actionsHarness) {
			h.a.Linker = func() (link.Linker, error) { return link.Linker{}, errors.New("opencode service status: exit 1") }
		}, "check_failed"},
		{"busy", mkBead("a", "in_progress", ""), func(h *actionsHarness) {
			h.a.inflight = map[BeadKey]string{{DB: "db", ID: "a"}: "dispatched"}
		}, "busy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, w, _ := newLinkActions(t, tt.bead)
			if tt.tweak != nil {
				tt.tweak(h)
			}
			_, err := invoke(h, "link", "a")
			wantFail(t, err, tt.typ)
			if len(w.set) != 0 {
				t.Fatalf("set = %v", w.set)
			}
		})
	}
	h, _, _ := newLinkActions(t)
	_, err := invoke(h, "link", "zz")
	wantFail(t, err, "unknown_bead")
}

func TestLinkRuleRefuses(t *testing.T) {
	h, w, _ := newLinkActions(t, mkBead("a", "in_progress", ""))
	w.tasks["a"] = tracker.Task{ID: "a", Status: "open", Metadata: map[string]any{}}
	_, err := invoke(h, "link", "a")
	re := wantFail(t, err, "not_linkable")
	if re.Message != "a: not in progress" {
		t.Fatalf("message = %q", re.Message)
	}
}
```

Append to `internal/btplugin/rpc_test.go`:

```go
func TestServerChoose(t *testing.T) {
	for result, want := range map[string]string{`"ses_a"`: "ses_a", "null": ""} {
		t.Run(result, func(t *testing.T) {
			hs, p := newHarness(t, nil)
			s := &Server{Peer: p}
			type answer struct {
				v   string
				err error
			}
			done := make(chan answer, 1)
			go func() {
				v, err := s.choose(context.Background(), SelectParams{Title: "t", Options: []SelectOption{{Value: "ses_a", Label: "A"}}})
				done <- answer{v, err}
			}()
			req := hs.read(t)
			if string(req["method"]) != `"ui.select"` || !strings.Contains(string(req["params"]), `"value":"ses_a"`) {
				t.Fatalf("req = %v", req)
			}
			hs.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"result":`+result+`}`)
			if got := <-done; got.err != nil || got.v != want {
				t.Fatalf("choose = %+v, want %q", got, want)
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/btplugin/`
Expected: FAIL to compile: `undefined: SelectParams`, unknown fields `Agents`, `Linker`, `Select`, `s.choose` undefined, `inflight` type mismatch.

- [ ] **Step 4: Add the protocol types**

In `internal/btplugin/protocol.go`, after `ConfirmParams`:

```go
// SelectParams are the params of a ui.select request to bt.
type SelectParams struct {
	Title   string         `json:"title"`
	Options []SelectOption `json:"options"`
}

type SelectOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
```

- [ ] **Step 5: Implement the action in `internal/btplugin/actions.go`**

Add the import `"github.com/AntoineGS/taskestrator/internal/link"` (`herdr`, `filepath` and `status` are already imported). Add fields to `Actions` after `Confirm`:

```go
	Agents  herdr.AgentLister                                        // nil when herdr is unavailable
	Linker  func() (link.Linker, error)                              // builds a linker; errors when OpenCode is unreachable
	Select  func(ctx context.Context, p SelectParams) (string, error) // asks the user to pick in bt; "" when dismissed
```

Change the `inflight` field and the `OnDispatched` comment:

```go
	inflight map[BeadKey]string // the action running on a bead: "dispatched" or "linked"
```

```go
	OnDispatched func(sessionID string)    // Loop.Refresh, after a dispatch or a link
```

In `Invoke`, add before the final `return`:

```go
	case "link":
		return a.linkSession(ctx, BeadKey{DB: p.Bead.DB, ID: p.Bead.ID})
```

Replace `acquire`:

```go
// acquire marks k as being doing ("dispatched" or "linked") unless an action
// is in flight on k or k was just dispatched; both checks and the mark are
// one atomic step.
func (a *Actions) acquire(k BeadKey, doing string) *RPCError {
	a.mu.Lock()
	defer a.mu.Unlock()
	if was, ok := a.inflight[k]; ok {
		return Fail("busy", k.ID+" is already being "+was)
	}
	if e, ok := a.sent[k]; ok {
		if !a.stale(k, e) {
			return errJustSent(k.ID)
		}
		delete(a.sent, k)
	}
	if a.inflight == nil {
		a.inflight = map[BeadKey]string{}
	}
	a.inflight[k] = doing
	return nil
}
```

In `dispatch`, change `a.acquire(k)` to `a.acquire(k, "dispatched")`. Any existing test that sets `inflight` as `map[BeadKey]bool` must change to `map[BeadKey]string` with the value `"dispatched"`.

Append:

```go
const opencodeSource = "herdr:opencode"

// linkSession links an untracked or lost bead to an OpenCode pane the user
// picks among those in the bead's checkout.
func (a *Actions) linkSession(ctx context.Context, k BeadKey) (InvokeResult, error) {
	b, ok := a.Engine.Bead(k)
	if !ok {
		return InvokeResult{}, Fail("unknown_bead", "unknown bead "+k.ID)
	}
	d, _ := a.Engine.Display(k)
	if d.State != status.Lost && (d.State != status.Untracked || b.Status != "in_progress") {
		return InvokeResult{}, Fail("not_linkable", fmt.Sprintf("%s is %s", k.ID, d.State))
	}
	repo := a.RepoFor(b)
	if repo == "" {
		return InvokeResult{}, Fail("no_repo", fmt.Sprintf("no checkout found for %s; add its parent to roots", k.DB))
	}
	if !a.herdrUp() || a.Agents == nil {
		return InvokeResult{}, Fail("no_herdr", "herdr is not running")
	}
	if a.Linker == nil || a.Select == nil {
		return InvokeResult{}, Fail("unavailable", "linking is not available")
	}
	if busy := a.acquire(k, "linked"); busy != nil {
		return InvokeResult{}, busy
	}
	defer a.finish(k, false, "")

	lk, err := a.Linker()
	if err != nil {
		return InvokeResult{}, Fail("check_failed", err.Error())
	}
	agents, err := a.Agents.Agents(ctx)
	if err != nil {
		return InvokeResult{}, Fail("check_failed", err.Error())
	}
	opts := sessionOptions(ctx, lk, repo, agents)
	if len(opts) == 0 {
		return InvokeResult{}, Fail("no_sessions", fmt.Sprintf("no OpenCode pane in %s; start one in herdr or claim the task from an agent", repo))
	}
	choice, err := a.Select(ctx, SelectParams{Title: "Link " + k.ID + " to an agent session", Options: opts})
	if err != nil {
		logger.Printf("link prompt for %s: %v", k.ID, err)
		return InvokeResult{}, nil
	}
	if choice == "" {
		return InvokeResult{}, nil
	}
	out, err := lk.Link(ctx, repo, k.ID, choice)
	if err != nil {
		return InvokeResult{}, Fail("check_failed", err.Error())
	}
	if out.Result != link.Linked && out.Result != link.Same {
		return InvokeResult{}, Fail("not_linkable", k.ID+": "+refusal(out.Result))
	}
	if a.OnDispatched != nil {
		a.OnDispatched(out.Root)
	}
	title := choice
	for _, o := range opts {
		if o.Value == choice {
			title = o.Label
		}
	}
	return InvokeResult{Toast: &Toast{Message: fmt.Sprintf("Linked %s to %s", k.ID, title), Tone: "ok"}}, nil
}

func refusal(r link.Result) string {
	switch r {
	case link.NotFound:
		return "bd has no such task"
	case link.NotInProgress:
		return "not in progress"
	case link.LiveSession:
		return "already linked to a live session"
	}
	return string(r)
}

// sessionOptions lists the OpenCode panes in repo as choices, once per
// session, leaving out sessions OpenCode cannot load.
func sessionOptions(ctx context.Context, lk link.Linker, repo string, agents []herdr.Agent) []SelectOption {
	var opts []SelectOption
	seen := map[string]bool{}
	for _, ag := range agents {
		if ag.Source != opencodeSource || ag.SessionID == "" || seen[ag.SessionID] || !herdr.Within(ag.Cwd, repo) {
			continue
		}
		seen[ag.SessionID] = true
		s, err := lk.OpenCode.GetSession(ctx, ag.SessionID)
		if err != nil {
			logger.Printf("link: session %s of pane %s: %v", ag.SessionID, ag.PaneID, err)
			continue
		}
		title := s.Title
		if title == "" {
			title = ag.SessionID
		}
		desc, err := filepath.Rel(repo, ag.Cwd)
		if err != nil {
			desc = ag.Cwd
		}
		if ag.Status != "" {
			desc += ", " + ag.Status
		}
		opts = append(opts, SelectOption{Value: ag.SessionID, Label: title, Description: desc})
	}
	return opts
}
```

- [ ] **Step 6: Implement `choose` and wire `Main` in `internal/btplugin/server.go`**

Add the import `"github.com/AntoineGS/taskestrator/internal/link"`. After `confirm`:

```go
// choose asks the user to pick an option in bt. A dismissed prompt is "".
func (s *Server) choose(ctx context.Context, p SelectParams) (string, error) {
	var v *string
	if err := s.Peer.Call(ctx, "ui.select", p, &v); err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return *v, nil
}
```

In `Main`, declare `var agents herdr.AgentLister` next to `var h herdr.Herdr`, set `agents = cli` right after `h = cli`, and add to the `Actions` literal:

```go
		Agents:       agents,
		Linker:       newLinker(ctx, bd),
		Select:       srv.choose,
```

Append:

```go
// newLinker builds Actions.Linker. Like dispatchDeps, it resolves the
// OpenCode service on every call.
func newLinker(ctx context.Context, bd link.Tracker) func() (link.Linker, error) {
	return func() (link.Linker, error) {
		c, err := opencode.FromService(ctx)
		if err != nil {
			return link.Linker{}, err
		}
		return link.Linker{Tracker: bd, OpenCode: c}, nil
	}
}
```

- [ ] **Step 7: Run tests**

Run: `make test && go test -race ./internal/btplugin/ && make lint`
Expected: PASS, race clean, lint clean.

- [ ] **Step 8: Commit**

```bash
git add internal/btplugin
git commit -m "feat: link action picks an OpenCode pane for an untracked task

Task: tsk-jqp"
```

---

### Task 8: Docs

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-10-07-bt-plugins-design.md`
- Modify: `docs/superpowers/specs/2026-10-09-session-link-design.md` (status line)
- Modify: `docs/contract-check.md`

- [ ] **Step 1: README**

In `README.md` under `## Use`, after the `P` bullet, add:

```markdown
- `K` links a task that is in progress but has no agent session (shown as
  "Not linked", `tsk.state = untracked`), or whose session is lost, to an
  OpenCode pane in its checkout: pick the session from the list.
```

In the sentence listing the states, change the end to:

```markdown
`unknown` (OpenCode unreachable), `queued` and `untracked` (in progress with
no linked session).
```

After the `### Bind bt to a key in herdr` section, add:

````markdown
### Link tasks agents claim by themselves

An agent you start by hand can claim a task with `bd update <id> --claim`
(or `--status in_progress`). The tsk OpenCode plugin then links the task to
the agent's session, so bt shows its state and `J` jumps to it. Install the
plugin once, then restart the OpenCode service:

```sh
ln -s "$PWD/opencode/tsk-link.js" ~/.config/opencode/plugins/tsk-link.js
opencode service restart
```

The plugin runs `tsk link` after each shell command that looks like a claim.
`tsk link` links a task only when bd reports it in progress, and never
replaces a session OpenCode still has. Each decision is a line in
`~/.local/state/tsk/link.log` (`$XDG_STATE_HOME/tsk/link.log` when set).
Claims it cannot see (an MCP tool, a script, `--db`) can be linked with `K`.
````

Add rows to the errors table:

```markdown
| no OpenCode pane in `<repo>`; start one in herdr or claim the task from an agent | `no_sessions` | `K` found no OpenCode pane in the task's checkout |
| `<id>` is `<state>` / `<id>`: not in progress | `not_linkable` | the task is not in progress without a live session |
```

- [ ] **Step 2: Older spec**

In `docs/superpowers/specs/2026-10-07-bt-plugins-design.md`, `### State mapping`, replace the row
``| untracked (no `tsk.session`, not open) | `state.clear` |`` with:

```markdown
| untracked, status `in_progress` | `untracked`, `[link]` (changed 2026-10-09, `tsk-jqp`) |
| untracked, other statuses | `state.clear` |
```

and change the lost row to ``| lost | `lost`, `[dispatch, link]` |``. Under `### Actions`, add:

```markdown
- `link` (added 2026-10-09, `tsk-jqp`): links an untracked or lost bead to an
  OpenCode pane in its checkout; see `2026-10-09-session-link-design.md`.
```

- [ ] **Step 3: Spec status**

In `docs/superpowers/specs/2026-10-09-session-link-design.md`, change `Status: draft, awaiting review` to `Status: implemented on feat/session-link`.

- [ ] **Step 4: Contract check**

Append to `docs/contract-check.md`:

```markdown
## Session linking (2026-10-09, bd 1.3.1, OpenCode 2.0.14)

| Check | Command | Observed | Result |
|---|---|---|---|
| bd show of a missing ID | `bd show tsk-zzzz --json` | Exit 1; stdout `{"error": "no issues found matching the provided IDs", "hint": ..., "schema_version": 1}`. Text that is not an ID gives the same answer. | PASS |
| herdr agent list fields | `herdr agent list` | Each agent has `agent`, `agent_session.{agent,kind,source,value}` (`source` `herdr:opencode`), `agent_status` (`idle`, `working`, `done`, ...), `cwd`, `pane_id`. | PASS |
```

(Task 9 adds the plugin rows.)

- [ ] **Step 5: Commit**

```bash
git add README.md docs
git commit -m "docs: session linking, the K action and the OpenCode plugin

Task: tsk-jqp"
```

---

### Task 9: End-to-end check against the real tools (needs the user's go-ahead)

This task changes the user's environment: it replaces `~/go/bin/tsk`, adds a file to `~/.config/opencode/plugins/`, and restarts the OpenCode service, which interrupts every running OpenCode session. **Ask the user before Step 1 and wait for a yes.**

- [ ] **Step 1: Install**

```sh
make install
ln -s "$PWD/opencode/tsk-link.js" ~/.config/opencode/plugins/tsk-link.js
opencode service restart
```

- [ ] **Step 2: Automatic link from a hand-started pane**

Create a probe task: `bd create --type chore --priority 4 --title "probe: tsk link e2e" --description "throwaway; close after the check" --json`, and note its ID (`<A>`). In herdr, open a tab in this worktree and run `opencode`; ask the agent: "Run exactly: bd update <A> --claim". Then check:

- `tail -n 3 ~/.local/state/tsk/link.log` shows `<A> linked ses_…`;
- `bd show <A> --json | jq '.[0].metadata'` has `tsk.session` (the pane's session) and `tsk.worktree`;
- in `bt -g`, `<A>` shows `RUN` or `WAIT`, and `J` focuses that pane.

- [ ] **Step 3: Re-claim from a dispatched session**

Create a second probe (`<B>`), press `D` on it in bt, and in its tab ask the agent: "Run exactly: bd update <B> --claim". `link.log` shows `<B> same ses_…` and the bd metadata is unchanged.

- [ ] **Step 4: Manual link**

Create a third probe (`<C>`) and claim it from a plain shell (`bd update <C> --claim`). In bt it shows "Not linked" (filter `tsk.state = untracked`). Press `K` and pick the pane from Step 2: the toast says `Linked <C> to …`, then live state and `J` work.

- [ ] **Step 5: Record and clean up**

Append rows to the `## Session linking` section of `docs/contract-check.md` for: the `execute.after` event reaching the plugin for a hand-started session (Step 2) and for an API-created session (Step 3), and the `K` flow (Step 4), each with what was observed and PASS/DEVIATES. Close the probes with `bd close <A> <B> <C> --reason "e2e probe"`. After confirming with the user, remove the worktree and branch `D` created, from the main checkout: `git worktree remove .worktrees/<B> && git branch -D task/<B>`.

```bash
git add docs/contract-check.md
git commit -m "docs: record the session-link end-to-end check

Task: tsk-jqp"
```

- [ ] **Step 6: Close the bead**

Write the close reason to a file under `/tmp` and run `bd close tsk-jqp --reason-file <file>` with Summary / Change / Files / Verify / Risk / Notes fields, in the style of `tsk-pg5`.
