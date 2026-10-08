package btplugin

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/status"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func mkBead(id, st, session string) Bead {
	b := Bead{DB: "db", ID: id, Status: st, Metadata: map[string]json.RawMessage{}}
	if session != "" {
		b.Metadata["tsk.session"] = json.RawMessage(`"` + session + `"`)
		b.Metadata["tsk.worktree"] = json.RawMessage(`"/w/` + id + `"`)
		b.Metadata["tsk.branch"] = json.RawMessage(`"br-` + id + `"`)
		b.Metadata["tsk.n"] = json.RawMessage(`5`)
	}
	return b
}

func envOK() Env {
	return Env{HerdrUp: true, RepoFor: func(Bead) string { return "/repo" }}
}

func newEng() (*Engine, *clock) {
	c := &clock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	return NewEngine(c.now), c
}

func one(t *testing.T, set []BeadState) BeadState {
	t.Helper()
	if len(set) != 1 {
		t.Fatalf("want 1 push, got %d: %+v", len(set), set)
	}
	return set[0]
}

func TestMapping(t *testing.T) {
	run := &status.Snapshot{Exists: true, Running: true}
	tests := []struct {
		name    string
		bead    Bead
		snap    *status.Snapshot
		failed  bool // snapshot load failed (stored nil)
		env     Env
		value   string // "" = cleared / not shown
		actions []string
	}{
		{"closed", mkBead("a", "closed", "s"), run, false, envOK(), "", nil},
		{"untracked", mkBead("a", "in_progress", ""), nil, false, envOK(), "", nil},
		{"review", mkBead("a", "review", "s"), run, false, envOK(), "review", nil},
		{"queued", mkBead("a", "open", ""), nil, false, envOK(), "queued", []string{"dispatch"}},
		{"queued no repo", mkBead("a", "open", ""), nil, false, Env{HerdrUp: true, RepoFor: func(Bead) string { return "" }}, "queued", nil},
		{"queued nil repofor", mkBead("a", "open", ""), nil, false, Env{HerdrUp: true}, "queued", nil},
		{"queued no herdr", mkBead("a", "open", ""), nil, false, Env{RepoFor: envOK().RepoFor}, "queued", nil},
		{"lost", mkBead("a", "in_progress", "s"), &status.Snapshot{}, false, envOK(), "lost", []string{"dispatch"}},
		{"lost no herdr", mkBead("a", "in_progress", "s"), &status.Snapshot{}, false, Env{RepoFor: envOK().RepoFor}, "lost", nil},
		{"waiting", mkBead("a", "in_progress", "s"), &status.Snapshot{Exists: true, Forms: 1}, false, envOK(), "waiting", []string{"jump"}},
		{"waiting no herdr", mkBead("a", "in_progress", "s"), &status.Snapshot{Exists: true}, false, Env{}, "waiting", nil},
		{"running", mkBead("a", "in_progress", "s"), run, false, envOK(), "running", []string{"jump"}},
		{"unknown", mkBead("a", "in_progress", "s"), nil, true, envOK(), "unknown", []string{"jump"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newEng()
			set, clear, _ := e.Sync([]Bead{tt.bead}, tt.env)
			if s := tt.bead.Metadata["tsk.session"]; s != nil && (tt.snap != nil || tt.failed) {
				set2, clear2 := e.SetSnapshots(map[string]*status.Snapshot{"s": tt.snap}, true, tt.env)
				set, clear = append(set, set2...), append(clear, clear2...)
			}
			if tt.value == "" {
				if len(set) != 0 || len(clear) != 0 {
					t.Fatalf("want nothing, got set=%v clear=%v", set, clear)
				}
				return
			}
			got := set[len(set)-1]
			if got.Fields["state"].Value != tt.value {
				t.Fatalf("value = %q, want %q", got.Fields["state"].Value, tt.value)
			}
			if strings.Join(got.Actions, ",") != strings.Join(tt.actions, ",") {
				t.Fatalf("actions = %v, want %v", got.Actions, tt.actions)
			}
			if tt.actions == nil && got.Actions != nil {
				t.Fatalf("actions should be nil, got %#v", got.Actions)
			}
		})
	}
}

func TestClearedWhenDone(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "open", "")}, envOK())
	set, clear, _ := e.Sync([]Bead{mkBead("a", "closed", "")}, envOK())
	if len(set) != 0 || len(clear) != 1 || clear[0] != (BeadKey{"db", "a"}) {
		t.Fatalf("set=%v clear=%v", set, clear)
	}
	set, clear, _ = e.Sync([]Bead{mkBead("a", "closed", "")}, envOK())
	if len(set)+len(clear) != 0 {
		t.Fatalf("second clear emitted: %v %v", set, clear)
	}
}

func TestSinceKeptAndReset(t *testing.T) {
	e, c := newEng()
	t0 := c.t
	b := mkBead("a", "in_progress", "s")
	e.Sync([]Bead{b}, envOK())
	c.t = t0.Add(30 * time.Second)
	t1 := c.t
	set, _ := e.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: true, Running: true}}, true, envOK())
	if got := one(t, set); !got.Fields["state"].Since.Equal(t1) {
		t.Fatalf("since = %v, want %v", got.Fields["state"].Since, t1)
	}
	c.t = t0.Add(time.Minute)
	// Same value, different detail (extra metadata) -> push, since kept.
	b2 := mkBead("a", "in_progress", "s")
	b2.Metadata["tsk.branch"] = json.RawMessage(`"other"`)
	set, _, _ = e.Sync([]Bead{b2}, envOK())
	got := one(t, set)
	if !got.Fields["state"].Since.Equal(t1) {
		t.Fatalf("since reset on unchanged value: %v", got.Fields["state"].Since)
	}
	c.t = t0.Add(2 * time.Minute)
	set, _ = e.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: true, Forms: 1}}, true, envOK())
	got = one(t, set)
	if got.Fields["state"].Value != "waiting" || !got.Fields["state"].Since.Equal(c.t) {
		t.Fatalf("since not reset on change: %+v", got.Fields["state"])
	}
}

func TestSecondSyncIdenticalEmitsNothing(t *testing.T) {
	e, _ := newEng()
	beads := []Bead{mkBead("a", "open", ""), mkBead("b", "review", "s")}
	set, _, _ := e.Sync(beads, envOK())
	if len(set) != 2 {
		t.Fatalf("first sync: %v", set)
	}
	set, clear, ns := e.Sync(beads, envOK())
	if len(set)+len(clear)+len(ns) != 0 {
		t.Fatalf("got %v %v %v", set, clear, ns)
	}
}

func TestRemovedBeadClearedOnce(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "open", ""), mkBead("b", "open", "")}, envOK())
	set, clear, _ := e.Sync([]Bead{mkBead("a", "open", "")}, envOK())
	if len(set) != 0 || len(clear) != 1 || clear[0].ID != "b" {
		t.Fatalf("set=%v clear=%v", set, clear)
	}
	_, clear, _ = e.Sync([]Bead{mkBead("a", "open", "")}, envOK())
	if len(clear) != 0 {
		t.Fatalf("cleared twice: %v", clear)
	}
	if _, ok := e.Bead(BeadKey{"db", "b"}); ok {
		t.Fatal("removed bead still present")
	}
}

func TestUnreachableThenRecover(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "in_progress", "s1"), mkBead("b", "in_progress", "s2")}, envOK())
	snaps := map[string]*status.Snapshot{"s1": {Exists: true, Running: true}, "s2": {Exists: true, Running: true}}
	e.SetSnapshots(snaps, true, envOK())
	set, _ := e.SetSnapshots(nil, false, envOK())
	if len(set) != 2 || set[0].Fields["state"].Value != "unknown" || set[1].Fields["state"].Value != "unknown" {
		t.Fatalf("not all unknown: %+v", set)
	}
	if e.Reachable() {
		t.Fatal("Reachable should be false")
	}
	if !strings.Contains(set[0].Sections["agent"], "OpenCode service unreachable.") {
		t.Fatalf("section = %q", set[0].Sections["agent"])
	}
	set, _ = e.SetSnapshots(snaps, true, envOK())
	if len(set) != 2 || set[0].Fields["state"].Value != "running" {
		t.Fatalf("not recovered: %+v", set)
	}
	// A failed load (nil) is unknown for that session only.
	set, _ = e.SetSnapshots(map[string]*status.Snapshot{"s1": nil}, true, envOK())
	if one(t, set).Fields["state"].Value != "unknown" {
		t.Fatalf("nil snapshot not unknown")
	}
}

func TestAgentSection(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "in_progress", "ses_1"), mkBead("q", "open", "")}, envOK())
	set, _ := e.SetSnapshots(map[string]*status.Snapshot{"ses_1": {Exists: true, Forms: 1, Question: "Use sqlite?"}}, true, envOK())
	got := one(t, set).Sections["agent"]
	want := "**Session** `ses_1`\n**Worktree** `/w/a`\n**Branch** `br-a`\n\nWaiting on you: question — Use sqlite?"
	if got != want {
		t.Fatalf("section:\n%q\nwant\n%q", got, want)
	}
	for _, s := range e.All(envOK()) {
		if s.ID == "q" && s.Sections != nil {
			t.Fatalf("queued bead has section: %v", s.Sections)
		}
	}
	e.SetSnapshots(map[string]*status.Snapshot{"ses_1": {Exists: true, Permissions: 1}}, true, envOK())
	if s := e.All(envOK()); !strings.HasSuffix(s[0].Sections["agent"], "Waiting on you: permission") {
		t.Fatalf("permission section: %q", s[0].Sections["agent"])
	}
	e.SetSnapshots(map[string]*status.Snapshot{"ses_1": {Exists: true}}, true, envOK())
	if s := e.All(envOK()); !strings.HasSuffix(s[0].Sections["agent"], "Waiting on you: idle") {
		t.Fatalf("idle section: %q", s[0].Sections["agent"])
	}
	e.SetSnapshots(map[string]*status.Snapshot{"ses_1": {}}, true, envOK())
	if s := e.All(envOK()); !strings.HasSuffix(s[0].Sections["agent"], "Session not found in OpenCode; dispatch again with D.") {
		t.Fatalf("lost section: %q", s[0].Sections["agent"])
	}
}

func TestNewSessionsAndSessions(t *testing.T) {
	e, _ := newEng()
	_, _, ns := e.Sync([]Bead{mkBead("a", "in_progress", "s1"), mkBead("r", "review", "s9"), mkBead("c", "closed", "s8")}, envOK())
	if len(ns) != 1 || ns[0] != "s1" {
		t.Fatalf("new = %v", ns)
	}
	_, _, ns = e.Sync([]Bead{mkBead("a", "in_progress", "s1"), mkBead("b", "in_progress", "s2")}, envOK())
	if len(ns) != 1 || ns[0] != "s2" {
		t.Fatalf("new = %v", ns)
	}
	if got := e.Sessions(); len(got) != 2 || got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("sessions = %v", got)
	}
	d, ok := e.Display(BeadKey{"db", "a"})
	if !ok || d.State != status.Unknown {
		t.Fatalf("display = %v %v", d, ok)
	}
}

func TestAllReplace(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "open", ""), mkBead("b", "closed", "")}, envOK())
	if got := e.All(envOK()); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("All = %+v", got)
	}
	if got := e.All(envOK()); len(got) != 1 {
		t.Fatalf("All not repeatable: %+v", got)
	}
}

func TestPendingSnapshotNotPushed(t *testing.T) {
	e, _ := newEng()
	set, clear, ns := e.Sync([]Bead{mkBead("a", "in_progress", "s")}, envOK())
	if len(set)+len(clear) != 0 || len(ns) != 1 {
		t.Fatalf("pending bead pushed: set=%v clear=%v ns=%v", set, clear, ns)
	}
	set, _ = e.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: true, Running: true}}, true, envOK())
	if one(t, set).Fields["state"].Value != "running" {
		t.Fatalf("not running: %+v", set)
	}
}

func TestFailedLoadIsUnknown(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "in_progress", "s")}, envOK())
	set, _ := e.SetSnapshots(map[string]*status.Snapshot{"s": nil}, true, envOK())
	got := one(t, set)
	if got.Fields["state"].Value != "unknown" {
		t.Fatalf("got %+v", got)
	}
	if sec := got.Sections["agent"]; !strings.HasSuffix(sec, "Could not load this session's state from OpenCode.") {
		t.Fatalf("section = %q", sec)
	}
}

func TestAllOmitsPending(t *testing.T) {
	e, _ := newEng()
	e.Sync([]Bead{mkBead("a", "in_progress", "s"), mkBead("q", "open", "")}, envOK())
	got := e.All(envOK())
	if len(got) != 1 || got[0].ID != "q" {
		t.Fatalf("All = %+v", got)
	}
}

func TestConcurrentUse(t *testing.T) {
	e, _ := newEng()
	beads := []Bead{mkBead("a", "in_progress", "s1"), mkBead("b", "open", ""), mkBead("c", "in_progress", "s2")}
	snaps := map[string]*status.Snapshot{"s1": {Exists: true, Running: true}, "s2": {Exists: true, Forms: 1}}
	var wg sync.WaitGroup
	run := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				f(i)
			}
		}()
	}
	run(func(int) { e.Sync(beads, envOK()) })
	run(func(i int) { e.SetSnapshots(snaps, i%7 != 0, envOK()) })
	run(func(int) { e.All(envOK()) })
	run(func(int) { e.Display(BeadKey{"db", "a"}); e.Bead(BeadKey{"db", "a"}); e.Reachable() })
	run(func(int) { e.Sessions() })
	wg.Wait()
}
