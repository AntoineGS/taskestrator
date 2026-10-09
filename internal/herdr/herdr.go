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

func parseErrorEnvelope(data []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	if env.Error.Message != "" {
		return env.Error.Message
	}
	var rawEnv struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &rawEnv); err != nil {
		return ""
	}
	if len(rawEnv.Error) > 0 && string(rawEnv.Error) != "null" {
		return string(rawEnv.Error)
	}
	return ""
}

func (h CLI) exec(ctx context.Context, args ...string) ([]byte, error) {
	data, err := h.Exec(ctx, "", args...)
	if err != nil {
		if errMsg := parseErrorEnvelope(data); errMsg != "" {
			return data, fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), errMsg, err)
		}
		return data, err
	}
	return data, nil
}

func (h CLI) call(ctx context.Context, out any, args ...string) error {
	data, err := h.exec(ctx, args...)
	if err != nil {
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

// Running reports whether the herdr server is up.
func (h CLI) Running(ctx context.Context) bool {
	data, err := h.exec(ctx, "status", "server")
	return err == nil && strings.Contains(string(data), "status: running")
}

// Within reports whether path is root or inside it.
func Within(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
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
		case Within(p.Cwd, repo):
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
	_, err := h.exec(ctx, args...)
	return err
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
	_, err := h.exec(ctx, args...)
	return err
}

// Agent is an agent pane as herdr reports it.
type Agent struct {
	PaneID    string
	Cwd       string
	SessionID string // "" when herdr has no session for the pane
	Source    string // who reported the session, such as "herdr:opencode"
	Status    string // herdr's agent_status: idle, working, blocked or done
}

// AgentLister lists agent panes.
type AgentLister interface {
	Agents(ctx context.Context) ([]Agent, error)
}

func (h CLI) Agents(ctx context.Context) ([]Agent, error) {
	var res struct {
		Agents []struct {
			PaneID  string `json:"pane_id"`
			Cwd     string `json:"cwd"`
			Status  string `json:"agent_status"`
			Session *struct {
				Value  string `json:"value"`
				Source string `json:"source"`
			} `json:"agent_session"`
		} `json:"agents"`
	}
	if err := h.call(ctx, &res, "agent", "list"); err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(res.Agents))
	for _, a := range res.Agents {
		ag := Agent{PaneID: a.PaneID, Cwd: a.Cwd, Status: a.Status}
		if a.Session != nil {
			ag.SessionID, ag.Source = a.Session.Value, a.Session.Source
		}
		out = append(out, ag)
	}
	return out, nil
}
