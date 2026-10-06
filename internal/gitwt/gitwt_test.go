package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func TestDefaultBranch(t *testing.T) {
	repo := newRepo(t)
	g := New()
	if b, err := g.DefaultBranch(context.Background(), repo); err != nil || b != "main" {
		t.Fatalf("no origin: %q, %v", b, err)
	}
	git(t, repo, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if b, err := g.DefaultBranch(context.Background(), repo); err != nil || b != "trunk" {
		t.Fatalf("origin/HEAD: %q, %v", b, err)
	}
}

func TestAddWorktreeCreatesBranch(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".worktrees", "se-1")
	added, err := New().AddWorktree(context.Background(), repo, path, "task/se-1", "main")
	if err != nil || added != (Added{Worktree: true, Branch: true}) {
		t.Fatalf("added = %+v, err = %v", added, err)
	}
	if b := git(t, path, "rev-parse", "--abbrev-ref", "HEAD"); b != "task/se-1" {
		t.Fatalf("branch = %q", b)
	}
}

func TestAddWorktreeReusesExisting(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".worktrees", "se-1")
	g := New()
	if _, err := g.AddWorktree(context.Background(), repo, path, "task/se-1", "main"); err != nil {
		t.Fatal(err)
	}
	added, err := g.AddWorktree(context.Background(), repo, path, "task/se-1", "main")
	if err != nil || added != (Added{}) {
		t.Fatalf("added = %+v, err = %v", added, err)
	}
}

func TestAddWorktreeUsesExistingBranch(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "task/se-2")
	added, err := New().AddWorktree(context.Background(), repo, filepath.Join(repo, ".worktrees", "se-2"), "task/se-2", "main")
	if err != nil || added != (Added{Worktree: true}) {
		t.Fatalf("added = %+v, err = %v", added, err)
	}
}

func TestAddWorktreeRejectsPathOnOtherBranch(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".worktrees", "se-3")
	git(t, repo, "worktree", "add", "-q", "-b", "other", path, "main")
	if _, err := New().AddWorktree(context.Background(), repo, path, "task/se-3", "main"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, ".worktrees", "se-4")
	g := New()
	if _, err := g.AddWorktree(context.Background(), repo, path, "task/se-4", "main"); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveWorktree(context.Background(), repo, path, "task/se-4", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if out := git(t, repo, "branch", "--list", "task/se-4"); out != "" {
		t.Fatalf("branch still exists: %q", out)
	}
}

func TestIsIgnored(t *testing.T) {
	repo := newRepo(t)
	g := New()
	if ok, err := g.IsIgnored(context.Background(), repo, ".worktrees/tsk-check"); err != nil || !ok {
		t.Fatalf("ignored: %v, %v", ok, err)
	}
	if ok, err := g.IsIgnored(context.Background(), repo, "src/main.go"); err != nil || ok {
		t.Fatalf("not ignored: %v, %v", ok, err)
	}
}
