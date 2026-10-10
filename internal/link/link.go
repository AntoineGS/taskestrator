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
