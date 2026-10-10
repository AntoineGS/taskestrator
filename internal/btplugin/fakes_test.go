package btplugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// recorder fakes every dispatch dependency. It logs calls in order and fails
// the call whose log entry starts with failOn.
type recorder struct {
	mu        sync.Mutex
	log       []string
	failOn    string
	noReview  bool
	unignored bool
	// ignoreNoEffect keeps .worktrees/ unignored after Ignore, as a negation
	// rule later in .gitignore would.
	ignoreNoEffect bool
	panes          map[string]string
	hasWS          bool
	block          chan struct{} // when set, claim waits on it
	entered        chan struct{} // signalled when claim starts

	readyCalls int

	reviewGate    chan struct{} // when set, the first HasReviewStatus call waits on it
	reviewEntered chan struct{} // signalled when that first HasReviewStatus call starts
	reviewCalls   int

	model *opencode.ModelRef // model of the last CreateSession

	agents []herdr.Agent // returned by Agents
}

func (r *recorder) note(format string, args ...any) error {
	entry := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.log = append(r.log, entry)
	r.mu.Unlock()
	if r.failOn != "" && strings.HasPrefix(entry, r.failOn) {
		return errors.New("injected failure")
	}
	return nil
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func (r *recorder) Ready(context.Context, string) ([]tracker.Task, error) {
	r.mu.Lock()
	r.readyCalls++
	r.mu.Unlock()
	return nil, nil
}
func (r *recorder) List(context.Context, string) ([]tracker.Task, error) { return nil, nil }
func (r *recorder) Show(_ context.Context, _, id string) (tracker.Task, error) {
	return tracker.Task{ID: id, Title: "Fix the thing"}, nil
}
func (r *recorder) Claim(_ context.Context, _, id string) error {
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.block != nil {
		<-r.block
	}
	return r.note("claim %s", id)
}
func (r *recorder) Unclaim(_ context.Context, _, id string) error {
	return r.note("unclaim %s", id)
}
func (r *recorder) SetMetadata(_ context.Context, _, id string, kv map[string]string) error {
	return r.note("setmeta %s %s", id, kv[tracker.MetaSession])
}
func (r *recorder) UnsetMetadata(_ context.Context, _, id string, keys ...string) error {
	return r.note("unsetmeta %s %s", id, strings.Join(keys, ","))
}
func (r *recorder) HasReviewStatus(context.Context, string) (bool, error) {
	r.mu.Lock()
	r.reviewCalls++
	first := r.reviewCalls == 1 && r.reviewGate != nil
	r.mu.Unlock()
	if first {
		r.reviewEntered <- struct{}{}
		<-r.reviewGate
	}
	return !r.noReview, nil
}
func (r *recorder) AddReviewStatus(context.Context, string) error {
	return r.note("addreviewstatus")
}

func (r *recorder) DefaultBranch(context.Context, string) (string, error) {
	return "main", r.note("defaultbranch")
}
func (r *recorder) AddWorktree(_ context.Context, _, path, branch, base string) (gitwt.Added, error) {
	return gitwt.Added{}, r.note("addworktree %s %s %s", path, branch, base)
}
func (r *recorder) RemoveWorktree(_ context.Context, _, path, _ string, deleteBranch bool) error {
	return r.note("removeworktree %s %v", path, deleteBranch)
}
func (r *recorder) IsIgnored(context.Context, string, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.unignored, nil
}
func (r *recorder) Ignore(_ context.Context, _, pattern string) error {
	if err := r.note("ignore %s", pattern); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ignoreNoEffect {
		r.unignored = false
	}
	return nil
}

func (r *recorder) CreateSession(_ context.Context, in opencode.CreateSession) (opencode.Session, error) {
	r.mu.Lock()
	r.model = in.Model
	r.mu.Unlock()
	if err := r.note("createsession %s", in.Directory); err != nil {
		return opencode.Session{}, err
	}
	return opencode.Session{ID: "ses_new"}, nil
}
func (r *recorder) DeleteSession(_ context.Context, id string) error {
	return r.note("deletesession %s", id)
}
func (r *recorder) Prompt(_ context.Context, id, _ string) error {
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
	return r.note("focus %s", pane)
}

func (r *recorder) shell(_ context.Context, dir string, args ...string) ([]byte, error) {
	return nil, r.note("shell %s %s", dir, strings.Join(args, " "))
}

func (r *recorder) deps() dispatch.Deps {
	return dispatch.Deps{Tracker: r, OpenCode: r, Herdr: r, Git: r, Shell: r.shell}
}

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
