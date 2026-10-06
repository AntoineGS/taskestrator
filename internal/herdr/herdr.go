// Package herdr is the only caller of the herdr CLI.
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AntoineGS/taskestrator/internal/execx"
)

// Herdr opens and focuses panes for tasks.
type Herdr interface {
	WorkspaceFor(ctx context.Context, repo string) (string, bool, error)
	CreateWorkspace(ctx context.Context, cwd, label string) (string, error)
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error)
	Run(ctx context.Context, paneID string, command ...string) error
	PaneForSession(ctx context.Context, sessionID string) (string, bool, error)
	Focus(ctx context.Context, paneID string) error
}

// CLI implements Herdr with the herdr binary.
type CLI struct{ Exec execx.Runner }

// New returns a CLI that runs the herdr binary on PATH.
func New() CLI { return CLI{Exec: execx.Command("herdr")} }

type pane struct {
	PaneID       string `json:"pane_id"`
	WorkspaceID  string `json:"workspace_id"`
	Cwd          string `json:"cwd"`
	AgentSession *struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

func (h CLI) parseErrorEnvelope(data []byte) string {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	if len(env.Error) > 0 && string(env.Error) != "null" {
		return string(env.Error)
	}
	return ""
}

func (h CLI) call(ctx context.Context, out any, args ...string) error {
	data, err := h.Exec(ctx, "", args...)
	if err != nil {
		if errMsg := h.parseErrorEnvelope(data); errMsg != "" {
			return fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), errMsg, err)
		}
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("herdr %s: unexpected output: %w", strings.Join(args, " "), err)
	}
	if len(env.Error) > 0 && string(env.Error) != "null" {
		return fmt.Errorf("herdr %s: %s", strings.Join(args, " "), env.Error)
	}
	return json.Unmarshal(env.Result, out)
}

func within(path, root string) bool {
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// WorkspaceFor returns a workspace with a pane in repo, preferring a pane at
// the repository root over one in a subdirectory or worktree.
func (h CLI) WorkspaceFor(ctx context.Context, repo string) (string, bool, error) {
	var res struct {
		Panes []pane `json:"panes"`
	}
	if err := h.call(ctx, &res, "pane", "list"); err != nil {
		return "", false, err
	}
	repo = filepath.Clean(repo)
	var exact, inside []string
	for _, p := range res.Panes {
		switch {
		case p.Cwd == "":
		case filepath.Clean(p.Cwd) == repo:
			exact = append(exact, p.WorkspaceID)
		case within(p.Cwd, repo):
			inside = append(inside, p.WorkspaceID)
		}
	}
	for _, ids := range [][]string{exact, inside} {
		if len(ids) > 0 {
			sort.Strings(ids)
			return ids[0], true, nil
		}
	}
	return "", false, nil
}

func (h CLI) CreateWorkspace(ctx context.Context, cwd, label string) (string, error) {
	var res struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := h.call(ctx, &res, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus"); err != nil {
		return "", err
	}
	return res.Workspace.ID, nil
}

func (h CLI) CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error) {
	var res struct {
		RootPane pane `json:"root_pane"`
	}
	if err := h.call(ctx, &res, "tab", "create", "--workspace", workspaceID, "--cwd", cwd, "--label", label, "--no-focus"); err != nil {
		return "", err
	}
	return res.RootPane.PaneID, nil
}

// Run passes command to herdr as a single string to avoid flag parsing issues.
// Arguments containing spaces must be pre-quoted by the caller.
func (h CLI) Run(ctx context.Context, paneID string, command ...string) error {
	args := []string{"pane", "run", paneID, strings.Join(command, " ")}
	data, err := h.Exec(ctx, "", args...)
	if err != nil {
		if errMsg := h.parseErrorEnvelope(data); errMsg != "" {
			return fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), errMsg, err)
		}
		return err
	}
	return nil
}

func (h CLI) PaneForSession(ctx context.Context, sessionID string) (string, bool, error) {
	var res struct {
		Agents []pane `json:"agents"`
	}
	if err := h.call(ctx, &res, "agent", "list"); err != nil {
		return "", false, err
	}
	for _, a := range res.Agents {
		if a.AgentSession != nil && a.AgentSession.Value == sessionID {
			return a.PaneID, true, nil
		}
	}
	return "", false, nil
}

func (h CLI) Focus(ctx context.Context, paneID string) error {
	args := []string{"agent", "focus", paneID}
	data, err := h.Exec(ctx, "", args...)
	if err != nil {
		if errMsg := h.parseErrorEnvelope(data); errMsg != "" {
			return fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), errMsg, err)
		}
		return err
	}
	return nil
}
