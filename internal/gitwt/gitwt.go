// Package gitwt is the only caller of git: per-task worktrees and branches.
package gitwt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/AntoineGS/taskestrator/internal/execx"
)

// Added reports what AddWorktree created, so an undo removes only that.
type Added struct {
	Worktree bool
	Branch   bool
}

// Git manages task worktrees.
type Git interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	AddWorktree(ctx context.Context, repo, path, branch, base string) (Added, error)
	RemoveWorktree(ctx context.Context, repo, path, branch string, deleteBranch bool) error
	IsIgnored(ctx context.Context, repo, rel string) (bool, error)
}

// CLI implements Git with the git binary.
type CLI struct{ Exec execx.Runner }

// New returns a CLI that runs git from PATH.
func New() CLI { return CLI{Exec: execx.Command("git")} }

// DefaultBranch returns the branch origin/HEAD points to, or "main".
func (g CLI) DefaultBranch(ctx context.Context, repo string) (string, error) {
	out, err := g.Exec(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		if execx.ExitCode(err) > 0 {
			return "main", nil
		}
		return "", err
	}
	ref := strings.TrimSpace(string(out))
	if _, branch, ok := strings.Cut(ref, "/"); ok {
		return branch, nil
	}
	return ref, nil
}

// AddWorktree creates path on branch, creating branch from base when needed.
// An existing worktree already on branch is reused.
func (g CLI) AddWorktree(ctx context.Context, repo, path, branch, base string) (Added, error) {
	if _, err := os.Stat(path); err == nil {
		out, err := g.Exec(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return Added{}, fmt.Errorf("%s exists but is not a git worktree: %w", path, err)
		}
		if cur := strings.TrimSpace(string(out)); cur != branch {
			return Added{}, fmt.Errorf("%s exists on branch %s, expected %s", path, cur, branch)
		}
		return Added{}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Added{}, err
	}
	_, err := g.Exec(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	switch {
	case err == nil:
		if _, err := g.Exec(ctx, repo, "worktree", "add", path, branch); err != nil {
			return Added{}, err
		}
		return Added{Worktree: true}, nil
	case execx.ExitCode(err) == 1:
		if _, err := g.Exec(ctx, repo, "worktree", "add", "-b", branch, path, base); err != nil {
			return Added{}, err
		}
		return Added{Worktree: true, Branch: true}, nil
	default:
		return Added{}, err
	}
}

// RemoveWorktree removes path and, when deleteBranch is set, its branch.
func (g CLI) RemoveWorktree(ctx context.Context, repo, path, branch string, deleteBranch bool) error {
	if _, err := g.Exec(ctx, repo, "worktree", "remove", "--force", path); err != nil {
		return err
	}
	if deleteBranch {
		if _, err := g.Exec(ctx, repo, "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

// IsIgnored reports whether rel is git-ignored in repo.
func (g CLI) IsIgnored(ctx context.Context, repo, rel string) (bool, error) {
	_, err := g.Exec(ctx, repo, "check-ignore", "--quiet", rel)
	if err == nil {
		return true, nil
	}
	if execx.ExitCode(err) == 1 {
		return false, nil
	}
	return false, err
}
