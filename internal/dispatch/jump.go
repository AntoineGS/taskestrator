package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Target identifies a dispatched task's session and where it runs.
type Target struct {
	Repo, Project, Worktree, TaskID, Title, SessionID string
}

// TargetFor builds a Target from a dispatched task.
func TargetFor(p discover.Project, t tracker.Task) Target {
	return Target{
		Repo: p.Repo, Project: p.Name, Worktree: t.Meta(tracker.MetaWorktree),
		TaskID: t.ID, Title: t.Title, SessionID: t.Meta(tracker.MetaSession),
	}
}

// TabLabel is the herdr tab label for a task: its ID and up to 30 characters of title.
func TabLabel(id, title string) string {
	const max = 30
	if r := []rune(title); len(r) > max {
		title = string(r[:max-1]) + "…"
	}
	return id + " " + title
}

// OpenTab opens a tab in the repository's workspace, creating the workspace
// if needed, and attaches OpenCode to the task's session.
func OpenTab(ctx context.Context, h herdr.Herdr, t Target) (string, error) {
	ws, ok, err := h.WorkspaceFor(ctx, t.Repo)
	if err != nil {
		return "", err
	}
	if !ok {
		if ws, err = h.CreateWorkspace(ctx, t.Repo, t.Project); err != nil {
			return "", err
		}
	}
	pane, err := h.CreateTab(ctx, ws, t.Worktree, TabLabel(t.TaskID, t.Title))
	if err != nil {
		return "", err
	}
	if err := h.Run(ctx, pane, "opencode", "--session", t.SessionID); err != nil {
		return "", err
	}
	return pane, nil
}

// A new tab's agent takes several seconds to register, and herdr refuses to
// focus it until then.
var (
	focusInterval = 500 * time.Millisecond
	focusTimeout  = 15 * time.Second
)

// Jump focuses the pane running the task's session, opening one when none exists.
func Jump(ctx context.Context, h herdr.Herdr, t Target) error {
	if t.SessionID == "" {
		return errors.New("task has not been dispatched")
	}
	pane, ok, err := h.PaneForSession(ctx, t.SessionID)
	if err != nil {
		return err
	}
	opened := !ok
	if !ok {
		if _, err := os.Stat(t.Worktree); err != nil {
			return fmt.Errorf("worktree %s is missing: %w", t.Worktree, err)
		}
		if pane, err = OpenTab(ctx, h, t); err != nil {
			return err
		}
	}
	return focus(ctx, h, pane, opened)
}

func focus(ctx context.Context, h herdr.Herdr, pane string, retry bool) error {
	deadline := time.Now().Add(focusTimeout)
	for {
		err := h.Focus(ctx, pane)
		if err == nil || !retry || !time.Now().Add(focusInterval).Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(focusInterval):
		}
	}
}
