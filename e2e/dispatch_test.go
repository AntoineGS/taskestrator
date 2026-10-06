//go:build e2e

// Package e2e dispatches a throwaway task against the real bd, OpenCode
// service and herdr. Run it with: make e2e
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/execx"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=tsk", "GIT_AUTHOR_EMAIL=tsk@localhost",
		"GIT_COMMITTER_NAME=tsk", "GIT_COMMITTER_EMAIL=tsk@localhost", "BD_NON_INTERACTIVE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDispatchEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	oc, err := opencode.FromService(ctx)
	if err != nil {
		t.Skipf("OpenCode service unavailable: %v", err)
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr is not on PATH")
	}

	repo := t.TempDir()
	sh(t, repo, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, repo, "git", "add", ".gitignore")
	sh(t, repo, "git", "commit", "-q", "-m", "init")
	prefix := "tske2e" + time.Now().Format("150405")
	sh(t, repo, "bd", "init", "--prefix", prefix, "--skip-agents", "--skip-hooks", "--non-interactive")
	id := sh(t, repo, "bd", "create", "Say ok", "--silent")

	bd := tracker.New()
	if err := bd.AddReviewStatus(ctx, repo); err != nil {
		t.Fatal(err)
	}
	h := herdr.New()
	tasks, err := bd.Ready(ctx, repo)
	if err != nil || len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("ready = %+v, err = %v", tasks, err)
	}

	res, err := dispatch.Run(ctx,
		dispatch.Deps{Tracker: bd, OpenCode: oc, Herdr: h, Git: gitwt.New(), Shell: execx.Command("sh")},
		dispatch.Request{
			Project: discover.Project{Name: prefix, Repo: repo},
			Task:    tasks[0],
			File:    config.ProjectFile{Prompt: "Reply with the single word ok. Do not run any tools."},
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = oc.DeleteSession(context.Background(), res.SessionID)
		if ws, ok, _ := h.WorkspaceFor(context.Background(), repo); ok {
			_ = exec.Command("herdr", "workspace", "close", ws).Run()
		}
	})
	if res.TabError != nil {
		t.Fatalf("tab: %v", res.TabError)
	}

	list, err := bd.List(ctx, repo)
	if err != nil || len(list) != 1 || list[0].Meta(tracker.MetaSession) != res.SessionID || list[0].Status != "in_progress" {
		t.Fatalf("tasks = %+v, err = %v", list, err)
	}
	if b := sh(t, res.Worktree, "git", "rev-parse", "--abbrev-ref", "HEAD"); b != "task/"+id {
		t.Fatalf("branch = %q", b)
	}
	if _, err := oc.GetSession(ctx, res.SessionID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if pane, ok, _ := h.PaneForSession(ctx, res.SessionID); ok && pane == res.PaneID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("herdr never reported the session in the new pane")
		}
		time.Sleep(time.Second)
	}
}
