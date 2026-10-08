package btplugin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	sent     map[BeadKey]sentEntry
	now      func() time.Time // time.Now when nil
}

// sentEntry records a finished dispatch until bt's sync shows it: was is the
// session the bead had before, and a bead still showing it is not yet updated.
type sentEntry struct {
	was string
	at  time.Time
}

const sentTTL = 2 * time.Minute

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

func (a *Actions) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// justSent reports whether k was dispatched and the engine has not caught up,
// pruning the entry once it has.
func (a *Actions) justSent(k BeadKey, b Bead) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.sent[k]
	if !ok {
		return false
	}
	if a.clock().Sub(e.at) >= sentTTL || metaString(b, tracker.MetaSession) != e.was || b.Status == "closed" {
		delete(a.sent, k)
		return false
	}
	return true
}

func (a *Actions) markSent(k BeadKey, was string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sent == nil {
		a.sent = map[BeadKey]sentEntry{}
	}
	a.sent[k] = sentEntry{was: was, at: a.clock()}
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
	if a.justSent(k, b) {
		return InvokeResult{}, Fail("busy", k.ID+" was just dispatched; waiting for bt to refresh")
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
	a.markSent(k, metaString(b, tracker.MetaSession))
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
	if d, _ := a.Engine.Display(k); d.State == status.Lost {
		return InvokeResult{}, Fail("session_lost", k.ID+"'s agent session is gone; press D to dispatch it again")
	}
	if !a.herdrUp() {
		return InvokeResult{}, Fail("no_herdr", "herdr is not running")
	}
	worktree := metaString(b, tracker.MetaWorktree)
	repo := a.RepoFor(b)
	if repo == "" {
		repo, _ = strings.CutSuffix(worktree, "/.worktrees/"+k.ID)
		if repo == worktree || !filepath.IsAbs(repo) {
			repo = ""
		}
	}
	if repo == "" {
		return InvokeResult{}, Fail("no_repo", fmt.Sprintf("cannot tell which checkout %s belongs to; add its parent to roots", k.ID))
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
