// Package dispatch starts an OpenCode agent on a Beads task.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/execx"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// OpenCode is the part of the OpenCode client dispatch uses.
type OpenCode interface {
	CreateSession(ctx context.Context, in opencode.CreateSession) (opencode.Session, error)
	DeleteSession(ctx context.Context, id string) error
	Prompt(ctx context.Context, id, text string) error
}

// Deps are dispatch's collaborators. Shell runs "sh".
type Deps struct {
	Tracker  tracker.Tracker
	OpenCode OpenCode
	Herdr    herdr.Herdr
	Git      gitwt.Git
	Shell    execx.Runner
}

// Request is one task to dispatch.
type Request struct {
	Project discover.Project
	Task    tracker.Task
	File    config.ProjectFile
}

// Result describes a dispatched task. TabError is set when the agent started
// but its herdr tab could not be opened.
type Result struct {
	SessionID string
	Worktree  string
	Branch    string
	PaneID    string
	TabError  error
}

// Run performs the spec's seven dispatch steps. On a failure before the
// prompt is sent, completed steps are undone in reverse order.
func Run(ctx context.Context, d Deps, req Request) (Result, error) {
	repo, id := req.Project.Repo, req.Task.ID
	branch := "task/" + id
	wt := filepath.Join(repo, ".worktrees", id)
	text, err := RenderPrompt(req.File.Prompt, PromptData{
		ID: id, Title: req.Task.Title, Description: req.Task.Description,
		Acceptance: req.Task.Acceptance, Worktree: wt, Branch: branch,
	})
	if err != nil {
		return Result{}, err
	}

	var undo []func(context.Context) error
	fail := func(step string, err error) (Result, error) {
		errs := []error{fmt.Errorf("%s: %w", step, err)}
		uctx := context.WithoutCancel(ctx)
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i](uctx); uerr != nil {
				errs = append(errs, fmt.Errorf("undo: %w", uerr))
			}
		}
		return Result{}, errors.Join(errs...)
	}

	if err := d.Tracker.Claim(ctx, repo, id); err != nil {
		return fail("claim", err)
	}
	undo = append(undo, func(c context.Context) error { return d.Tracker.Unclaim(c, repo, id) })

	base, err := d.Git.DefaultBranch(ctx, repo)
	if err != nil {
		return fail("default branch", err)
	}
	added, err := d.Git.AddWorktree(ctx, repo, wt, branch, base)
	if err != nil {
		return fail("worktree", err)
	}
	if added.Worktree {
		undo = append(undo, func(c context.Context) error {
			return d.Git.RemoveWorktree(c, repo, wt, branch, added.Branch)
		})
	}

	if req.File.Setup != "" {
		if _, err := d.Shell(ctx, wt, "-c", req.File.Setup); err != nil {
			return fail("setup", err)
		}
	}

	sess, err := d.OpenCode.CreateSession(ctx, opencode.CreateSession{
		Title: id + ": " + req.Task.Title, Directory: wt, Metadata: map[string]string{"tsk.task": id},
	})
	if err != nil {
		return fail("session", err)
	}
	undo = append(undo, func(c context.Context) error { return d.OpenCode.DeleteSession(c, sess.ID) })

	meta := map[string]string{tracker.MetaSession: sess.ID, tracker.MetaWorktree: wt, tracker.MetaBranch: branch}
	if err := d.Tracker.SetMetadata(ctx, repo, id, meta); err != nil {
		return fail("record", err)
	}
	undo = append(undo, func(c context.Context) error {
		return d.Tracker.UnsetMetadata(c, repo, id, tracker.MetaSession, tracker.MetaWorktree, tracker.MetaBranch)
	})

	if err := d.OpenCode.Prompt(ctx, sess.ID, text); err != nil {
		return fail("prompt", err)
	}

	res := Result{SessionID: sess.ID, Worktree: wt, Branch: branch}
	pane, err := OpenTab(ctx, d.Herdr, Target{
		Repo: repo, Project: req.Project.Name, Worktree: wt, TaskID: id, Title: req.Task.Title, SessionID: sess.ID,
	})
	if err != nil {
		res.TabError = err
		return res, nil
	}
	res.PaneID = pane
	return res, nil
}
