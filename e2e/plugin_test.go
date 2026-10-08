//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/btplugin"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// wireMsg is any JSON-RPC message the plugin writes.
type wireMsg struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type pluginConn struct {
	t      *testing.T
	stdin  interface{ Write([]byte) (int, error) }
	msgs   chan wireMsg
	nextID int
}

func (c *pluginConn) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write to plugin: %v", err)
	}
}

func (c *pluginConn) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and returns its response, skipping notifications that
// arrive first.
func (c *pluginConn) call(method string, params any, timeout time.Duration) wireMsg {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-c.msgs:
			if !ok {
				c.t.Fatalf("plugin exited before answering %s", method)
			}
			if m.ID != nil && *m.ID == id {
				return m
			}
		case <-deadline:
			c.t.Fatalf("no response to %s within %v", method, timeout)
		}
	}
}

// waitState reads messages until a state.set carries want for the bead, and
// returns the actions listed with it.
func (c *pluginConn) waitState(id string, want []string, timeout time.Duration) (string, []string) {
	c.t.Helper()
	deadline := time.After(timeout)
	last := "(none)"
	for {
		select {
		case m, ok := <-c.msgs:
			if !ok {
				c.t.Fatalf("plugin exited while waiting for %v (last state %s)", want, last)
			}
			if m.Method != "state.set" {
				continue
			}
			var p btplugin.StateSetParams
			if err := json.Unmarshal(m.Params, &p); err != nil {
				c.t.Fatalf("state.set params: %v", err)
			}
			for _, b := range p.Beads {
				if b.ID != id {
					continue
				}
				last = b.Fields["state"].Value
				for _, w := range want {
					if last == w {
						return last, b.Actions
					}
				}
			}
		case <-deadline:
			c.t.Fatalf("bead never reached %v within %v (last state %s)", want, timeout, last)
		}
	}
}

func TestPluginEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	oc, err := opencode.FromService(ctx)
	if err != nil {
		t.Skipf("OpenCode service unavailable: %v", err)
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr is not on PATH")
	}
	h := herdr.New()
	if !h.Running(ctx) {
		t.Skip("herdr server is not running; start herdr first")
	}

	bin := filepath.Join(t.TempDir(), "tsk")
	build := exec.Command("go", "build", "-o", bin, "../cmd/tsk")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tsk: %v: %s", err, out)
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
	if err := os.WriteFile(filepath.Join(repo, ".tsk.toml"),
		[]byte("prompt = \"Reply with the single word ok. Do not run any tools.\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bd := tracker.New()
	if err := bd.AddReviewStatus(ctx, repo); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		// The session is read back from the bead so a failure after dispatch
		// cannot leak it.
		if task, err := bd.Show(cctx, repo, id); err == nil {
			if sid := task.Meta(tracker.MetaSession); sid != "" {
				_ = oc.DeleteSession(cctx, sid)
			}
		}
		if ws, ok, _ := h.WorkspaceFor(cctx, repo); ok {
			_ = exec.CommandContext(cctx, "herdr", "workspace", "close", ws).Run()
		}
		wt := filepath.Join(repo, ".worktrees", id)
		_ = exec.CommandContext(cctx, "git", "-C", repo, "worktree", "remove", "--force", wt).Run()
		closeCmd := exec.CommandContext(cctx, "bd", "close", id)
		closeCmd.Dir = repo
		closeCmd.Env = append(os.Environ(), "BD_NON_INTERACTIVE=1")
		_ = closeCmd.Run()
	})

	cmd := exec.CommandContext(ctx, bin, "plugin")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "BD_NON_INTERACTIVE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &pluginConn{t: t, stdin: stdin, msgs: make(chan wireMsg, 256)}
	go func() {
		defer close(c.msgs)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			var m wireMsg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("plugin wrote a non-protocol line: %q", sc.Text())
				continue
			}
			c.msgs <- m
		}
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	res := c.call("initialize", map[string]any{
		"protocolVersion": 1,
		"bt":              map[string]any{"version": "0.0.0-e2e"},
		"scope":           map[string]any{"mode": "project", "databases": []string{prefix}},
		"popup":           false,
		"options":         map[string]any{"roots": []string{filepath.Dir(repo)}},
	}, 15*time.Second)
	if res.Error != nil {
		t.Fatalf("initialize: %+v", res.Error)
	}
	var manifest btplugin.PluginManifest
	if err := json.Unmarshal(res.Result, &manifest); err != nil || manifest.Name != "tsk" {
		t.Fatalf("manifest = %s, err = %v", res.Result, err)
	}

	bead := func(status string, meta map[string]any) map[string]any {
		return map[string]any{
			"db": prefix, "id": id, "repo": repo, "status": status, "title": "Say ok",
			"type": "task", "priority": 2, "assignee": "", "metadata": meta,
		}
	}
	c.notify("beads.sync", map[string]any{"revision": 1, "beads": []any{bead("open", map[string]any{})}})
	state, actions := c.waitState(id, []string{"queued"}, 30*time.Second)
	if !contains(actions, "dispatch") {
		t.Fatalf("state %s actions = %v, want dispatch", state, actions)
	}

	ref := btplugin.BeadRef{DB: prefix, ID: id, Repo: &repo}
	res = c.call("action.invoke", btplugin.InvokeParams{Action: "dispatch", Bead: ref, View: "board"}, 90*time.Second)
	if res.Error != nil {
		t.Fatalf("dispatch: %+v", res.Error)
	}
	var inv btplugin.InvokeResult
	if err := json.Unmarshal(res.Result, &inv); err != nil || inv.Toast == nil || !strings.Contains(inv.Toast.Message, "Dispatched") {
		t.Fatalf("dispatch result = %s, err = %v", res.Result, err)
	}

	task, err := bd.Show(ctx, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := task.Meta(tracker.MetaSession)
	if sessionID == "" {
		t.Fatalf("no %s metadata after dispatch: %+v", tracker.MetaSession, task.Metadata)
	}
	if _, err := oc.GetSession(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{}
	for k, v := range task.Metadata {
		if strings.HasPrefix(k, "tsk.") {
			meta[k] = v
		}
	}
	c.notify("beads.sync", map[string]any{"revision": 2, "beads": []any{bead("in_progress", meta)}})
	state, actions = c.waitState(id, []string{"running", "waiting"}, 60*time.Second)
	t.Logf("bead %s is %s, actions %v", id, state, actions)

	res = c.call("action.invoke", btplugin.InvokeParams{Action: "jump", Bead: ref, View: "board"}, 30*time.Second)
	if res.Error != nil {
		t.Fatalf("jump: %+v", res.Error)
	}
	inv = btplugin.InvokeResult{}
	if err := json.Unmarshal(res.Result, &inv); err != nil || !inv.Dismiss {
		t.Fatalf("jump result = %s, err = %v", res.Result, err)
	}

	if res := c.call("shutdown", nil, 10*time.Second); res.Error != nil {
		t.Fatalf("shutdown: %+v", res.Error)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
