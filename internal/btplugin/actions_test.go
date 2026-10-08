package btplugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/status"
)

type actionsHarness struct {
	a          *Actions
	r          *recorder
	dispatched []string
}

func newActions(t *testing.T, repo string, beads ...Bead) *actionsHarness {
	t.Helper()
	r := &recorder{ready: []string{"a"}, panes: map[string]string{}}
	eng, _ := newEng()
	eng.Sync(beads, Env{HerdrUp: true, RepoFor: func(Bead) string { return repo }})
	h := &actionsHarness{r: r}
	h.a = &Actions{
		Engine:  eng,
		Tracker: r,
		Git:     r,
		Deps:    func() (dispatch.Deps, error) { return r.deps(), nil },
		Herdr:   r,
		HerdrUp: func() bool { return true },
		RepoFor: func(Bead) string { return repo },
		OnDispatched: func(s string) {
			h.dispatched = append(h.dispatched, s)
		},
	}
	return h
}

func invoke(h *actionsHarness, action, id string) (InvokeResult, error) {
	return h.a.Invoke(context.Background(), InvokeParams{Action: action, Bead: BeadRef{DB: "db", ID: id}})
}

func wantFail(t *testing.T, err error, typ string) *RPCError {
	t.Helper()
	var re *RPCError
	if !errors.As(err, &re) {
		t.Fatalf("want RPCError %s, got %v", typ, err)
	}
	var data struct {
		Type string `json:"type"`
	}
	raw, _ := json.Marshal(re.Data)
	_ = json.Unmarshal(raw, &data)
	if data.Type != typ {
		t.Fatalf("want type %s, got %s (%s)", typ, data.Type, re.Message)
	}
	return re
}

func TestDispatchRefusals(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		beads []Bead
		tweak func(h *actionsHarness)
		typ   string
		msg   string
	}{
		{"unknown bead", "zz", []Bead{mkBead("a", "open", "")}, nil, "unknown_bead", ""},
		{"running", "a", []Bead{mkBead("a", "in_progress", "s")}, func(h *actionsHarness) {
			h.a.Engine.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: true, Running: true}}, true, Env{HerdrUp: true, RepoFor: func(Bead) string { return "/repo" }})
		}, "not_dispatchable", "a is in progress"},
		{"no repo", "a", []Bead{mkBead("a", "open", "")}, func(h *actionsHarness) {
			h.a.RepoFor = func(Bead) string { return "" }
		}, "no_repo", "no checkout found for db; add its parent to roots"},
		{"no herdr", "a", []Bead{mkBead("a", "open", "")}, func(h *actionsHarness) {
			h.a.HerdrUp = func() bool { return false }
		}, "no_herdr", "herdr is not running"},
		{"not ready", "a", []Bead{mkBead("a", "open", "")}, func(h *actionsHarness) { h.r.ready = nil }, "not_ready", "a is blocked"},
		{"no review status", "a", []Bead{mkBead("a", "open", "")}, func(h *actionsHarness) { h.r.noReview = true }, "no_review_status", `bd config set status.custom "review:wip"`},
		{"worktrees not ignored", "a", []Bead{mkBead("a", "open", "")}, func(h *actionsHarness) { h.r.unignored = true }, "worktrees_not_ignored", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newActions(t, "/repo", tt.beads...)
			if tt.tweak != nil {
				tt.tweak(h)
			}
			_, err := invoke(h, "dispatch", tt.id)
			re := wantFail(t, err, tt.typ)
			if !strings.Contains(re.Message, tt.msg) {
				t.Fatalf("message %q lacks %q", re.Message, tt.msg)
			}
			if len(h.r.calls()) != 0 || len(h.dispatched) != 0 {
				t.Fatalf("refusal must not dispatch: %v", h.r.calls())
			}
		})
	}
}

func TestDispatchHappy(t *testing.T) {
	repo := t.TempDir()
	h := newActions(t, repo, mkBead("a", "open", ""))
	res, err := invoke(h, "dispatch", "a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Toast == nil || res.Toast.Tone != "ok" || res.Toast.Message != "Dispatched a (session ses_new)" {
		t.Fatalf("toast = %+v", res.Toast)
	}
	wt := filepath.Join(repo, ".worktrees", "a")
	want := []string{"claim a", "defaultbranch", "addworktree " + wt + " task/a main", "createsession " + wt, "setmeta a ses_new", "prompt ses_new"}
	got := h.r.calls()
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("log = %v", got)
	}
	if got[len(got)-1] != "run w2:p9 opencode --session ses_new" {
		t.Fatalf("tab not opened: %v", got)
	}
	if !slices.Equal(h.dispatched, []string{"ses_new"}) {
		t.Fatalf("dispatched = %v", h.dispatched)
	}
	if slices.Contains(got, "addreviewstatus") {
		t.Fatal("must never add the review status")
	}
}

func TestDispatchTabError(t *testing.T) {
	h := newActions(t, t.TempDir(), mkBead("a", "open", ""))
	h.r.failOn = "createworkspace"
	res, err := invoke(h, "dispatch", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Toast.Message, "; tab not opened: injected failure") {
		t.Fatalf("toast = %q", res.Toast.Message)
	}
	if len(h.dispatched) != 1 {
		t.Fatal("OnDispatched not called")
	}
}

func TestDispatchFailed(t *testing.T) {
	h := newActions(t, t.TempDir(), mkBead("a", "open", ""))
	h.r.failOn = "createsession"
	_, err := invoke(h, "dispatch", "a")
	re := wantFail(t, err, "dispatch_failed")
	if !strings.Contains(re.Message, "session: injected failure") {
		t.Fatalf("message = %q", re.Message)
	}
	if len(h.dispatched) != 0 {
		t.Fatal("OnDispatched must not be called")
	}
	h = newActions(t, t.TempDir(), mkBead("a", "open", ""))
	h.a.Deps = func() (dispatch.Deps, error) { return dispatch.Deps{}, errors.New("opencode unreachable") }
	_, err = invoke(h, "dispatch", "a")
	wantFail(t, err, "dispatch_failed")
}

func TestDispatchLostSkipsReady(t *testing.T) {
	h := newActions(t, t.TempDir(), mkBead("a", "in_progress", "s"))
	h.a.Engine.SetSnapshots(map[string]*status.Snapshot{"s": {Exists: false}}, true, Env{HerdrUp: true, RepoFor: func(Bead) string { return "/repo" }})
	h.r.ready = nil
	if _, err := invoke(h, "dispatch", "a"); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchBusy(t *testing.T) {
	h := newActions(t, t.TempDir(), mkBead("a", "open", ""))
	h.r.block = make(chan struct{})
	h.r.entered = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { _, err := invoke(h, "dispatch", "a"); done <- err }()
	<-h.r.entered
	_, err := invoke(h, "dispatch", "a")
	wantFail(t, err, "busy")
	close(h.r.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.r.block = nil
	if _, err := invoke(h, "dispatch", "a"); err != nil {
		t.Fatalf("in-flight mark not released: %v", err)
	}
}

func TestJump(t *testing.T) {
	h := newActions(t, "/repo", mkBead("a", "in_progress", "s"))
	h.r.panes["s"] = "w1:p1"
	res, err := invoke(h, "jump", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dismiss {
		t.Fatal("jump must dismiss")
	}
	if got := h.r.calls(); !slices.Equal(got, []string{"paneforsession s", "focus w1:p1"}) {
		t.Fatalf("log = %v", got)
	}
}

func TestJumpRefusals(t *testing.T) {
	h := newActions(t, "/repo", mkBead("a", "open", ""), mkBead("b", "in_progress", "s"))
	_, err := invoke(h, "jump", "a")
	wantFail(t, err, "not_dispatched")
	h.a.Herdr = nil
	_, err = invoke(h, "jump", "b")
	wantFail(t, err, "no_herdr")
	_, err = invoke(h, "jump", "zz")
	wantFail(t, err, "unknown_bead")
	_, err = invoke(h, "bogus", "a")
	wantFail(t, err, "unknown_action")
}

func TestJumpDerivesRepoFromWorktree(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "a")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	b := mkBead("a", "in_progress", "s")
	b.Metadata["tsk.worktree"] = json.RawMessage(`"` + wt + `"`)
	h := newActions(t, "", b)
	if _, err := invoke(h, "jump", "a"); err != nil {
		t.Fatal(err)
	}
	got := h.r.calls()
	if !slices.Contains(got, "createworkspace "+repo+" db") {
		t.Fatalf("repo not derived from worktree: %v", got)
	}
}
