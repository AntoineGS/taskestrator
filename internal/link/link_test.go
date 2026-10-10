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
