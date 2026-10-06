package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

type fakeGit struct{ ignored bool }

func (g fakeGit) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (g fakeGit) AddWorktree(context.Context, string, string, string, string) (gitwt.Added, error) {
	return gitwt.Added{}, nil
}
func (g fakeGit) RemoveWorktree(context.Context, string, string, string, bool) error { return nil }
func (g fakeGit) IsIgnored(context.Context, string, string) (bool, error)            { return g.ignored, nil }

type dispatchRecorder struct {
	mu   sync.Mutex
	reqs []dispatch.Request
	res  dispatch.Result
	err  error
}

func (d *dispatchRecorder) run(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = append(d.reqs, req)
	return d.res, d.err
}

func newTestProject(t *testing.T, ignored, hasReview bool) (*Project, *fakeTracker, *dispatchRecorder, *jumpRecorder) {
	t.Helper()
	ready := tracker.Task{ID: "se-r", Title: "Ready one", Status: "open", Priority: 1}
	tr := &fakeTracker{
		hasReview: hasReview,
		tasks: map[string][]tracker.Task{"/se": {
			ready,
			dispatched("se-l", "Lost one", "in_progress", "ses_gone"),
			dispatched("se-w", "Working", "in_progress", "ses_w"),
			{ID: "se-m", Title: "Manual", Status: "in_progress"},
			{ID: "se-c", Title: "Closed", Status: "closed"},
		}},
		ready: map[string][]tracker.Task{"/se": {ready}},
	}
	lv := &fakeLive{active: map[string]bool{"ses_w": true}, snaps: map[string]status.Snapshot{"ses_w": {Exists: true, Running: true}}}
	d := &dispatchRecorder{res: dispatch.Result{SessionID: "ses_new"}}
	j := &jumpRecorder{}
	p := NewProject(context.Background(), ProjectConfig{
		Project: se, File: config.ProjectFile{Setup: "make deps"}, Tracker: tr, Live: lv,
		Jump: j.jump, Dispatch: d.run, Git: fakeGit{ignored: ignored},
		Reread: 10 * time.Second, After: noAfter,
	})
	return p, tr, d, j
}

func cursorTo(t *testing.T, p *Project, id string) {
	t.Helper()
	for i, r := range p.rows {
		if r.task.ID == id {
			p.cursor, p.selected = i, id
			return
		}
	}
	t.Fatalf("no row %s", id)
}

func TestProjectListsReadyFirstAndHidesClosed(t *testing.T) {
	p, _, _, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	v := view(m)
	if strings.Index(v, "se-r") > strings.Index(v, "se-w") {
		t.Fatalf("the ready task should come first:\n%s", v)
	}
	for _, want := range []string{"queued", "in progress", "lost", "untracked (in_progress)"} {
		if !strings.Contains(v, want) {
			t.Errorf("view is missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "se-c") {
		t.Errorf("closed task shown:\n%s", v)
	}
}

func TestProjectDispatchesReadyTask(t *testing.T) {
	p, tr, d, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-r")
	before := tr.listCount("/se")
	m, _ = press(t, m, "d")
	if len(d.reqs) != 1 || d.reqs[0].Task.ID != "se-r" || d.reqs[0].File.Setup != "make deps" || d.reqs[0].Project.Repo != "/se" {
		t.Fatalf("reqs = %+v", d.reqs)
	}
	if !strings.Contains(view(m), "dispatched se-r") || tr.listCount("/se") <= before {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestProjectRedispatchesLostTask(t *testing.T) {
	p, _, d, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-l")
	press(t, m, "d")
	if len(d.reqs) != 1 || d.reqs[0].Task.ID != "se-l" {
		t.Fatalf("reqs = %+v", d.reqs)
	}
}

func TestProjectRefusesUntrackedDispatch(t *testing.T) {
	p, _, d, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	for _, id := range []string{"se-m", "se-w"} {
		cursorTo(t, p, id)
		m, _ = press(t, m, "d")
	}
	if len(d.reqs) != 0 {
		t.Fatalf("dispatched %+v", d.reqs)
	}
	if !strings.Contains(view(m), "not ready to dispatch") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestProjectRequiresReviewStatusAndIgnoredWorktrees(t *testing.T) {
	p, tr, d, _ := newTestProject(t, false, false)
	m, _ := settle(t, p, p.Init())
	v := view(m)
	if !strings.Contains(v, "no review status") || !strings.Contains(v, ".worktrees/ is not git-ignored") {
		t.Fatalf("warnings missing:\n%s", v)
	}
	cursorTo(t, p, "se-r")
	m, _ = press(t, m, "d")
	if len(d.reqs) != 0 {
		t.Fatal("dispatched without the review status")
	}
	m, _ = press(t, m, "R")
	if !tr.added {
		t.Fatal("R did not add the review status")
	}
	m, _ = press(t, m, "d")
	if len(d.reqs) != 0 {
		t.Fatal("dispatched while .worktrees/ is not ignored")
	}
	if !strings.Contains(view(m), ".gitignore") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestProjectReportsTabErrorAndDispatchError(t *testing.T) {
	p, _, d, _ := newTestProject(t, true, true)
	d.res.TabError = errors.New("no herdr")
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-r")
	m, _ = press(t, m, "d")
	if !strings.Contains(view(m), "could not open its tab") {
		t.Fatalf("view:\n%s", view(m))
	}
	d.err, d.res = errors.New("claim: taken"), dispatch.Result{}
	cursorTo(t, p, "se-l")
	m, _ = press(t, m, "d")
	if !strings.Contains(view(m), "claim: taken") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestProjectEnterJumps(t *testing.T) {
	p, _, _, j := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-w")
	_, msgs := press(t, m, "enter")
	if len(j.targets) != 1 || j.targets[0].SessionID != "ses_w" || !quits(msgs) {
		t.Fatalf("targets = %+v", j.targets)
	}
}

func TestProjectDoesNotDispatchTwiceBeforeReload(t *testing.T) {
	p, _, d, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-r")
	m, _ = press(t, m, "d")
	cursorTo(t, p, "se-r")
	m, _ = press(t, m, "d")
	if len(d.reqs) != 1 {
		t.Fatalf("reqs = %+v", d.reqs)
	}
	if !strings.Contains(view(m), "already dispatched") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestProjectRetriesAfterFailedDispatch(t *testing.T) {
	p, _, d, _ := newTestProject(t, true, true)
	d.err = errors.New("claim: taken")
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-r")
	m, _ = press(t, m, "d")
	cursorTo(t, p, "se-r")
	press(t, m, "d")
	if len(d.reqs) != 2 {
		t.Fatalf("reqs = %+v", d.reqs)
	}
}

func TestProjectCursorFollowsTaskAcrossReload(t *testing.T) {
	p, tr, d, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	for i := 0; p.rows[p.cursor].task.ID != "se-l"; i++ {
		if i > len(p.rows) {
			t.Fatal("se-l not found")
		}
		m, _ = press(t, m, "j")
	}
	newer := tracker.Task{ID: "se-a", Title: "Newly ready", Status: "open", Priority: 0}
	tr.tasks["/se"] = append([]tracker.Task{newer}, tr.tasks["/se"]...)
	tr.ready["/se"] = append([]tracker.Task{newer}, tr.ready["/se"]...)
	m, _ = settle(t, m, func() tea.Msg { return tickMsg{} })
	if got := p.rows[p.cursor].task.ID; got != "se-l" {
		t.Fatalf("after reload the cursor is on %s, want se-l:\n%s", got, view(m))
	}
	press(t, m, "d")
	if len(d.reqs) != 1 || d.reqs[0].Task.ID != "se-l" {
		t.Fatalf("reqs = %+v", d.reqs)
	}
}

func TestProjectQuitIsBlockedWhileDispatching(t *testing.T) {
	p, _, _, _ := newTestProject(t, true, true)
	m, _ := settle(t, p, p.Init())
	cursorTo(t, p, "se-r")
	release := make(chan struct{})
	p.cfg.Dispatch = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		<-release
		return dispatch.Result{}, nil
	}
	m, _ = m.Update(key("d"))
	for _, k := range []string{"q", "esc", "ctrl+c"} {
		_, cmd := m.Update(key(k))
		if cmd != nil && quits(collect(cmd)) {
			t.Errorf("%s quit during a dispatch", k)
		}
	}
	if !strings.Contains(view(m), "dispatch in progress") {
		t.Fatalf("view:\n%s", view(m))
	}
	close(release)
}

func TestProjectUnreachableBannerWaitsForSnapshots(t *testing.T) {
	p, _, _, _ := newTestProject(t, true, true)
	if strings.Contains(view(p), "unreachable") {
		t.Fatalf("banner shown before any snapshot result:\n%s", view(p))
	}
	p.cfg.Live.(*fakeLive).set(func(f *fakeLive) { f.down = true })
	m, _ := settle(t, p, p.Init())
	v := view(m)
	if !strings.Contains(v, "unreachable") || !strings.Contains(v, "connection refused") {
		t.Fatalf("banner missing:\n%s", v)
	}
}
