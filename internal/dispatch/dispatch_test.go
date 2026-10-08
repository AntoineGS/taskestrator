package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// recorder fakes every dispatch dependency. It logs calls in order and fails
// the call whose log entry starts with failOn.
type recorder struct {
	log        []string
	failOn     string
	added      gitwt.Added
	panes      map[string]string
	hasWS      bool
	focusFails int
	promptIn   string
}

func (r *recorder) note(format string, args ...any) error {
	entry := fmt.Sprintf(format, args...)
	r.log = append(r.log, entry)
	if r.failOn != "" && strings.HasPrefix(entry, r.failOn) {
		return errors.New("injected failure")
	}
	return nil
}

func (r *recorder) Ready(context.Context, string) ([]tracker.Task, error) { return nil, nil }
func (r *recorder) List(context.Context, string) ([]tracker.Task, error)  { return nil, nil }
func (r *recorder) Show(context.Context, string, string) (tracker.Task, error) {
	return tracker.Task{}, nil
}
func (r *recorder) Claim(_ context.Context, _, id string) error {
	return r.note("claim %s", id)
}
func (r *recorder) Unclaim(_ context.Context, _, id string) error {
	return r.note("unclaim %s", id)
}
func (r *recorder) SetMetadata(_ context.Context, _, id string, kv map[string]string) error {
	return r.note("setmeta %s %s %s %s", id, kv[tracker.MetaSession], kv[tracker.MetaWorktree], kv[tracker.MetaBranch])
}
func (r *recorder) UnsetMetadata(_ context.Context, _, id string, keys ...string) error {
	return r.note("unsetmeta %s %s", id, strings.Join(keys, ","))
}
func (r *recorder) HasReviewStatus(context.Context, string) (bool, error) { return true, nil }
func (r *recorder) AddReviewStatus(context.Context, string) error         { return nil }

func (r *recorder) DefaultBranch(context.Context, string) (string, error) {
	return "main", r.note("defaultbranch")
}
func (r *recorder) AddWorktree(_ context.Context, _, path, branch, base string) (gitwt.Added, error) {
	if err := r.note("addworktree %s %s %s", path, branch, base); err != nil {
		return gitwt.Added{}, err
	}
	return r.added, nil
}
func (r *recorder) RemoveWorktree(_ context.Context, _, path, _ string, deleteBranch bool) error {
	return r.note("removeworktree %s %v", path, deleteBranch)
}
func (r *recorder) IsIgnored(context.Context, string, string) (bool, error) { return true, nil }

func (r *recorder) CreateSession(_ context.Context, in opencode.CreateSession) (opencode.Session, error) {
	if err := r.note("createsession %s %s %s", in.Directory, in.Title, in.Metadata["tsk.task"]); err != nil {
		return opencode.Session{}, err
	}
	return opencode.Session{ID: "ses_new"}, nil
}
func (r *recorder) DeleteSession(_ context.Context, id string) error {
	return r.note("deletesession %s", id)
}
func (r *recorder) Prompt(_ context.Context, id, text string) error {
	r.promptIn = text
	return r.note("prompt %s", id)
}

func (r *recorder) WorkspaceFor(context.Context, string) (string, bool, error) {
	return "w1", r.hasWS, r.note("workspacefor")
}
func (r *recorder) CreateWorkspace(_ context.Context, cwd, label string) (string, error) {
	return "w2", r.note("createworkspace %s %s", cwd, label)
}
func (r *recorder) CreateTab(_ context.Context, ws, cwd, label string) (string, error) {
	return ws + ":p9", r.note("createtab %s %s %s", ws, cwd, label)
}
func (r *recorder) Run(_ context.Context, pane string, command ...string) error {
	return r.note("run %s %s", pane, strings.Join(command, " "))
}
func (r *recorder) PaneForSession(_ context.Context, id string) (string, bool, error) {
	p, ok := r.panes[id]
	return p, ok, r.note("paneforsession %s", id)
}
func (r *recorder) Focus(_ context.Context, pane string) error {
	if err := r.note("focus %s", pane); err != nil {
		return err
	}
	if r.focusFails > 0 {
		r.focusFails--
		return errors.New("agent_not_found")
	}
	return nil
}

func (r *recorder) shell(_ context.Context, dir string, args ...string) ([]byte, error) {
	return nil, r.note("shell %s %s", dir, strings.Join(args, " "))
}

func deps(r *recorder) Deps {
	return Deps{Tracker: r, OpenCode: r, Herdr: r, Git: r, Shell: r.shell}
}

var req = Request{
	Project: discover.Project{Name: "se", Repo: "/r"},
	Task:    tracker.Task{ID: "se-1", Title: "Fix the thing", Description: "Details.", Acceptance: "It works."},
	File:    config.ProjectFile{Setup: "make deps"},
}

const wt = "/r/.worktrees/se-1"

var fullLog = []string{
	"claim se-1",
	"defaultbranch",
	"addworktree " + wt + " task/se-1 main",
	"shell " + wt + " -c make deps",
	"createsession " + wt + " se-1: Fix the thing se-1",
	"setmeta se-1 ses_new " + wt + " task/se-1",
	"prompt ses_new",
	"workspacefor",
	"createworkspace /r se",
	"createtab w2 " + wt + " se-1 Fix the thing",
	"run w2:p9 opencode --session ses_new",
}

func TestRunSuccess(t *testing.T) {
	r := &recorder{added: gitwt.Added{Worktree: true, Branch: true}}
	res, err := Run(context.Background(), deps(r), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.log, "\n") != strings.Join(fullLog, "\n") {
		t.Fatalf("log:\n%s", strings.Join(r.log, "\n"))
	}
	if res != (Result{SessionID: "ses_new", Worktree: wt, Branch: "task/se-1", PaneID: "w2:p9"}) {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"se-1: Fix the thing", "Details.", "It works.", "bd update se-1 --status review", "Never push"} {
		if !strings.Contains(r.promptIn, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestRunUndoesInReverseOrder(t *testing.T) {
	rm := "removeworktree " + wt + " true"
	undo := map[string][]string{
		"claim":         nil,
		"defaultbranch": {"unclaim se-1"},
		"addworktree":   {"unclaim se-1"},
		"shell":         {rm, "unclaim se-1"},
		"createsession": {rm, "unclaim se-1"},
		"setmeta":       {"deletesession ses_new", rm, "unclaim se-1"},
		"prompt":        {"unsetmeta se-1 tsk.session,tsk.worktree,tsk.branch", "deletesession ses_new", rm, "unclaim se-1"},
	}
	for step, want := range undo {
		r := &recorder{failOn: step, added: gitwt.Added{Worktree: true, Branch: true}}
		_, err := Run(context.Background(), deps(r), req)
		if err == nil || !strings.Contains(err.Error(), "injected failure") {
			t.Errorf("%s: err = %v", step, err)
			continue
		}
		i := 0
		for i < len(r.log) && !strings.HasPrefix(r.log[i], step) {
			i++
		}
		if got := r.log[i+1:]; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: undo = %v, want %v", step, got, want)
		}
	}
}

func TestRunDoesNotRemoveReusedWorktree(t *testing.T) {
	r := &recorder{failOn: "createsession", added: gitwt.Added{}}
	if _, err := Run(context.Background(), deps(r), req); err == nil {
		t.Fatal("expected an error")
	}
	for _, entry := range r.log {
		if strings.HasPrefix(entry, "removeworktree") {
			t.Fatalf("removed a reused worktree: %v", r.log)
		}
	}
}

func TestRunTabFailureKeepsAgentRunning(t *testing.T) {
	r := &recorder{failOn: "createtab", added: gitwt.Added{Worktree: true}, hasWS: true}
	res, err := Run(context.Background(), deps(r), req)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.TabError == nil || res.SessionID != "ses_new" {
		t.Fatalf("result = %+v", res)
	}
	for _, entry := range r.log {
		if strings.HasPrefix(entry, "unclaim") || strings.HasPrefix(entry, "deletesession") {
			t.Fatalf("undid work after the prompt: %v", r.log)
		}
	}
}

func TestRunBadTemplateTouchesNothing(t *testing.T) {
	r := &recorder{}
	bad := req
	bad.File.Prompt = "{{.Nope"
	if _, err := Run(context.Background(), deps(r), bad); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.log) != 0 {
		t.Fatalf("log = %v", r.log)
	}
}

func TestRunSkipsEmptySetup(t *testing.T) {
	r := &recorder{added: gitwt.Added{Worktree: true}}
	noSetup := req
	noSetup.File.Setup = ""
	if _, err := Run(context.Background(), deps(r), noSetup); err != nil {
		t.Fatal(err)
	}
	for _, entry := range r.log {
		if strings.HasPrefix(entry, "shell") {
			t.Fatalf("ran setup: %v", r.log)
		}
	}
}

func TestJumpFocusesExistingPane(t *testing.T) {
	r := &recorder{panes: map[string]string{"ses_a": "w3:p1"}}
	if err := Jump(context.Background(), r, Target{SessionID: "ses_a"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.log, "|") != "paneforsession ses_a|focus w3:p1" {
		t.Fatalf("log = %v", r.log)
	}
}

func TestJumpOpensTabWhenClosed(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{hasWS: true}
	tgt := Target{Repo: "/r", Project: "se", Worktree: dir, TaskID: "se-1", Title: "Fix", SessionID: "ses_a"}
	if err := Jump(context.Background(), r, tgt); err != nil {
		t.Fatal(err)
	}
	want := "paneforsession ses_a|workspacefor|createtab w1 " + dir + " se-1 Fix|run w1:p9 opencode --session ses_a|focus w1:p9"
	if strings.Join(r.log, "|") != want {
		t.Fatalf("log = %v", r.log)
	}
}

func fastFocus(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	oldInterval, oldTimeout := focusInterval, focusTimeout
	focusInterval, focusTimeout = interval, timeout
	t.Cleanup(func() { focusInterval, focusTimeout = oldInterval, oldTimeout })
}

func TestJumpRetriesFocusAfterReopening(t *testing.T) {
	fastFocus(t, time.Millisecond, time.Second)
	r := &recorder{hasWS: true, focusFails: 3}
	tgt := Target{Repo: "/r", Project: "se", Worktree: t.TempDir(), TaskID: "se-1", Title: "Fix", SessionID: "ses_a"}
	if err := Jump(context.Background(), r, tgt); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(r.log, "|"), "focus w1:p9"); n != 4 {
		t.Fatalf("focus attempts = %d, log = %v", n, r.log)
	}
}

func TestJumpGivesUpWhenFocusNeverSucceeds(t *testing.T) {
	fastFocus(t, time.Millisecond, 10*time.Millisecond)
	r := &recorder{hasWS: true, focusFails: 1 << 30}
	tgt := Target{Repo: "/r", Project: "se", Worktree: t.TempDir(), TaskID: "se-1", Title: "Fix", SessionID: "ses_a"}
	err := Jump(context.Background(), r, tgt)
	if err == nil || !strings.Contains(err.Error(), "agent_not_found") {
		t.Fatalf("err = %v", err)
	}
}

func TestJumpDoesNotRetryExistingPane(t *testing.T) {
	r := &recorder{panes: map[string]string{"ses_a": "w3:p1"}, focusFails: 1 << 30}
	if err := Jump(context.Background(), r, Target{SessionID: "ses_a"}); err == nil {
		t.Fatal("expected the focus error")
	}
	if n := strings.Count(strings.Join(r.log, "|"), "focus"); n != 1 {
		t.Fatalf("focus attempts = %d", n)
	}
}

func TestJumpMissingWorktree(t *testing.T) {
	r := &recorder{}
	err := Jump(context.Background(), r, Target{Worktree: "/nonexistent/wt", SessionID: "ses_a"})
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/wt") {
		t.Fatalf("err = %v", err)
	}
}

func TestTabLabel(t *testing.T) {
	if got := TabLabel("se-1", strings.Repeat("x", 40)); got != "se-1 "+strings.Repeat("x", 29)+"…" {
		t.Fatalf("got %q", got)
	}
}

func TestTargetFor(t *testing.T) {
	task := tracker.Task{ID: "se-1", Title: "Fix", Metadata: map[string]any{tracker.MetaSession: "ses_a", tracker.MetaWorktree: wt}}
	got := TargetFor(discover.Project{Name: "se", Repo: "/r"}, task)
	if got != (Target{Repo: "/r", Project: "se", Worktree: wt, TaskID: "se-1", Title: "Fix", SessionID: "ses_a"}) {
		t.Fatalf("got %+v", got)
	}
}

// hungOpenCode blocks the named steps until their context ends.
type hungOpenCode struct {
	*recorder
	create, prompt, del bool
}

func wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (h hungOpenCode) CreateSession(ctx context.Context, in opencode.CreateSession) (opencode.Session, error) {
	if h.create {
		return opencode.Session{}, wait(ctx)
	}
	return h.recorder.CreateSession(ctx, in)
}

func (h hungOpenCode) Prompt(ctx context.Context, id, text string) error {
	if h.prompt {
		return wait(ctx)
	}
	return h.recorder.Prompt(ctx, id, text)
}

func (h hungOpenCode) DeleteSession(ctx context.Context, id string) error {
	if h.del {
		return wait(ctx)
	}
	return h.recorder.DeleteSession(ctx, id)
}

func TestRunBoundsOpenCodeSteps(t *testing.T) {
	old := stepTimeout
	stepTimeout = 20 * time.Millisecond
	t.Cleanup(func() { stepTimeout = old })
	for name, h := range map[string]hungOpenCode{
		"createsession": {create: true},
		"prompt":        {prompt: true},
		"undo":          {prompt: true, del: true},
	} {
		h.recorder = &recorder{added: gitwt.Added{Worktree: true, Branch: true}}
		d := deps(h.recorder)
		d.OpenCode = h
		_, err := Run(context.Background(), d, req)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v", name, err)
		}
		if last := h.recorder.log[len(h.recorder.log)-1]; last != "unclaim se-1" {
			t.Errorf("%s: undo did not finish, log = %v", name, h.recorder.log)
		}
	}
}
