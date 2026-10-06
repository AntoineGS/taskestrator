// Package tracker is the only caller of the bd CLI.
package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AntoineGS/taskestrator/internal/execx"
)

const (
	MetaSession  = "tsk.session"
	MetaWorktree = "tsk.worktree"
	MetaBranch   = "tsk.branch"
	StatusReview = "review"
)

// Task is a Beads issue as returned by bd --json.
type Task struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Acceptance  string         `json:"acceptance_criteria"`
	Status      string         `json:"status"`
	Priority    int            `json:"priority"`
	Assignee    string         `json:"assignee"`
	Metadata    map[string]any `json:"metadata"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ClosedAt    *time.Time     `json:"closed_at"`
}

// Meta returns a string metadata value, or "" when absent or not a string.
func (t Task) Meta(key string) string {
	if v, ok := t.Metadata[key].(string); ok {
		return v
	}
	return ""
}

// Tracker reads and updates a project's tasks.
type Tracker interface {
	Ready(ctx context.Context, repo string) ([]Task, error)
	List(ctx context.Context, repo string) ([]Task, error)
	Claim(ctx context.Context, repo, id string) error
	Unclaim(ctx context.Context, repo, id string) error
	SetMetadata(ctx context.Context, repo, id string, kv map[string]string) error
	UnsetMetadata(ctx context.Context, repo, id string, keys ...string) error
	HasReviewStatus(ctx context.Context, repo string) (bool, error)
	AddReviewStatus(ctx context.Context, repo string) error
}

// BD implements Tracker with the bd CLI, run inside the repository.
type BD struct{ Run execx.Runner }

// New returns a BD that runs the bd binary on PATH.
func New() BD { return BD{Run: execx.Command("bd")} }

func (b BD) Ready(ctx context.Context, repo string) ([]Task, error) {
	return b.tasks(ctx, repo, "ready", "--json", "-n", "0")
}

func (b BD) List(ctx context.Context, repo string) ([]Task, error) {
	return b.tasks(ctx, repo, "list", "--json", "--all", "-n", "0")
}

func (b BD) tasks(ctx context.Context, repo string, args ...string) ([]Task, error) {
	out, err := b.Run(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(out, &tasks); err != nil {
		return nil, fmt.Errorf("bd %s: unexpected output: %w", args[0], err)
	}
	return tasks, nil
}

func (b BD) Claim(ctx context.Context, repo, id string) error {
	_, err := b.Run(ctx, repo, "update", id, "--claim")
	return err
}

func (b BD) Unclaim(ctx context.Context, repo, id string) error {
	_, err := b.Run(ctx, repo, "update", id, "--status", "open", "--assignee", "")
	return err
}

func (b BD) SetMetadata(ctx context.Context, repo, id string, kv map[string]string) error {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{"update", id}
	for _, k := range keys {
		args = append(args, "--set-metadata", k+"="+kv[k])
	}
	_, err := b.Run(ctx, repo, args...)
	return err
}

func (b BD) UnsetMetadata(ctx context.Context, repo, id string, keys ...string) error {
	args := []string{"update", id}
	for _, k := range keys {
		args = append(args, "--unset-metadata", k)
	}
	_, err := b.Run(ctx, repo, args...)
	return err
}

func (b BD) customStatuses(ctx context.Context, repo string) (string, error) {
	out, err := b.Run(ctx, repo, "config", "get", "status.custom", "--json")
	if err != nil {
		return "", err
	}
	var v struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("bd config get status.custom: unexpected output: %w", err)
	}
	return strings.TrimSpace(v.Value), nil
}

func hasReview(custom string) bool {
	for _, entry := range strings.Split(custom, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(entry), ":")
		if name == StatusReview {
			return true
		}
	}
	return false
}

func (b BD) HasReviewStatus(ctx context.Context, repo string) (bool, error) {
	custom, err := b.customStatuses(ctx, repo)
	if err != nil {
		return false, err
	}
	return hasReview(custom), nil
}

func (b BD) AddReviewStatus(ctx context.Context, repo string) error {
	custom, err := b.customStatuses(ctx, repo)
	if err != nil {
		return err
	}
	if hasReview(custom) {
		return nil
	}
	value := StatusReview + ":wip"
	if custom != "" {
		value = custom + "," + value
	}
	_, err = b.Run(ctx, repo, "config", "set", "status.custom", value)
	return err
}
