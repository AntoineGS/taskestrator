package btplugin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Actions runs the plugin's dispatch and jump actions.
type Actions struct {
	Engine       *Engine
	Tracker      tracker.Tracker
	Git          gitwt.Git
	Deps         func() (dispatch.Deps, error) // builds deps; errors when OpenCode is unreachable
	Herdr        herdr.Herdr                   // nil when herdr unavailable
	HerdrUp      func() bool
	RepoFor      func(b Bead) string
	OnDispatched func(sessionID string) // Loop.Refresh

	mu       sync.Mutex
	inflight map[BeadKey]bool
}

// Invoke runs one action. Refusals are returned as Fail errors.
func (a *Actions) Invoke(ctx context.Context, p InvokeParams) (InvokeResult, error) {
	switch p.Action {
	case "dispatch":
		return a.dispatch(ctx, BeadKey{DB: p.Bead.DB, ID: p.Bead.ID})
	case "jump":
		return a.jump(ctx, BeadKey{DB: p.Bead.DB, ID: p.Bead.ID})
	}
	return InvokeResult{}, Fail("unknown_action", "unknown action "+p.Action)
}

func (a *Actions) herdrUp() bool {
	return a.Herdr != nil && a.HerdrUp != nil && a.HerdrUp()
}

func (a *Actions) acquire(k BeadKey) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inflight[k] {
		return false
	}
	if a.inflight == nil {
		a.inflight = map[BeadKey]bool{}
	}
	a.inflight[k] = true
	return true
}

func (a *Actions) release(k BeadKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inflight, k)
}

func (a *Actions) dispatch(ctx context.Context, k BeadKey) (InvokeResult, error) {
	b, ok := a.Engine.Bead(k)
	if !ok {
		return InvokeResult{}, Fail("unknown_bead", "unknown bead "+k.ID)
	}
	d, _ := a.Engine.Display(k)
	if d.State != status.Queued && d.State != status.Lost {
		return InvokeResult{}, Fail("not_dispatchable", fmt.Sprintf("%s is %s", k.ID, d.State))
	}
	repo := a.RepoFor(b)
	if repo == "" {
		return InvokeResult{}, Fail("no_repo", fmt.Sprintf("no checkout found for %s; add its parent to roots", k.DB))
	}
	if !a.herdrUp() {
		return InvokeResult{}, Fail("no_herdr", "herdr is not running")
	}
	if d.State == status.Queued {
		ready, err := a.Tracker.Ready(ctx, repo)
		if err != nil {
			return InvokeResult{}, Fail("check_failed", err.Error())
		}
		found := false
		for _, t := range ready {
			if t.ID == k.ID {
				found = true
			}
		}
		if !found {
			return InvokeResult{}, Fail("not_ready", k.ID+" is blocked")
		}
	}
	hasReview, err := a.Tracker.HasReviewStatus(ctx, repo)
	if err != nil {
		return InvokeResult{}, Fail("check_failed", err.Error())
	}
	if !hasReview {
		return InvokeResult{}, Fail("no_review_status", `this project has no review status; run: bd config set status.custom "review:wip"`)
	}
	ignored, err := a.Git.IsIgnored(ctx, repo, ".worktrees/tsk-check")
	if err != nil {
		return InvokeResult{}, Fail("check_failed", err.Error())
	}
	if !ignored {
		return InvokeResult{}, Fail("worktrees_not_ignored", ".worktrees/ is not git-ignored in this repo; add it to .gitignore first")
	}
	if !a.acquire(k) {
		return InvokeResult{}, Fail("busy", k.ID+" is already being dispatched")
	}
	defer a.release(k)

	task, err := a.Tracker.Show(ctx, repo, k.ID)
	if err != nil {
		return InvokeResult{}, Fail("dispatch_failed", err.Error())
	}
	file, err := config.LoadProjectFile(repo)
	if err != nil {
		return InvokeResult{}, Fail("dispatch_failed", err.Error())
	}
	deps, err := a.Deps()
	if err != nil {
		return InvokeResult{}, Fail("dispatch_failed", err.Error())
	}
	res, err := dispatch.Run(ctx, deps, dispatch.Request{
		Project: discover.Project{Name: k.DB, Repo: repo}, Task: task, File: file,
	})
	if err != nil {
		return InvokeResult{}, Fail("dispatch_failed", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	msg := fmt.Sprintf("Dispatched %s (session %s)", k.ID, res.SessionID)
	if res.TabError != nil {
		msg += "; tab not opened: " + res.TabError.Error()
	}
	if a.OnDispatched != nil {
		a.OnDispatched(res.SessionID)
	}
	return InvokeResult{Toast: &Toast{Message: msg, Tone: "ok"}}, nil
}

func (a *Actions) jump(ctx context.Context, k BeadKey) (InvokeResult, error) {
	b, ok := a.Engine.Bead(k)
	if !ok {
		return InvokeResult{}, Fail("unknown_bead", "unknown bead "+k.ID)
	}
	session := metaString(b, tracker.MetaSession)
	if session == "" {
		return InvokeResult{}, Fail("not_dispatched", k.ID+" has not been dispatched")
	}
	if !a.herdrUp() {
		return InvokeResult{}, Fail("no_herdr", "herdr is not running")
	}
	worktree := metaString(b, tracker.MetaWorktree)
	repo := a.RepoFor(b)
	if repo == "" {
		repo, _ = strings.CutSuffix(worktree, "/.worktrees/"+k.ID)
		if repo == worktree {
			repo = ""
		}
	}
	task := tracker.Task{ID: k.ID, Title: b.Title, Metadata: map[string]any{
		tracker.MetaSession: session, tracker.MetaWorktree: worktree,
	}}
	target := dispatch.TargetFor(discover.Project{Name: k.DB, Repo: repo}, task)
	if err := dispatch.Jump(ctx, a.Herdr, target); err != nil {
		return InvokeResult{}, Fail("jump_failed", err.Error())
	}
	return InvokeResult{Dismiss: true}, nil
}
