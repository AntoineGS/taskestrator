package herdr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeRun struct {
	calls []string
	args  [][]string
	out   map[string]string
}

func (f *fakeRun) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	f.args = append(f.args, args)
	return []byte(f.out[k]), nil
}

const panes = `{"id":"cli:pane:list","result":{"type":"pane_list","panes":[
{"pane_id":"w2:p1","workspace_id":"w2","tab_id":"w2:t1","cwd":"/src/other"},
{"pane_id":"w9:p3","workspace_id":"w9","tab_id":"w9:t2","cwd":"/src/repo/.worktrees/se-1"},
{"pane_id":"w5:p1","workspace_id":"w5","tab_id":"w5:t1","cwd":"/src/repo"},
{"pane_id":"w7:p1","workspace_id":"w7","tab_id":"w7:t1","cwd":"/src/repository"}]}}`

func TestWorkspaceForPrefersExactMatch(t *testing.T) {
	f := &fakeRun{out: map[string]string{"pane list": panes}}
	h := CLI{Exec: f.run}
	ws, ok, err := h.WorkspaceFor(context.Background(), "/src/repo")
	if err != nil || !ok || ws != "w5" {
		t.Fatalf("ws = %q, ok = %v, err = %v", ws, ok, err)
	}
	if _, ok, _ := h.WorkspaceFor(context.Background(), "/src/none"); ok {
		t.Fatal("matched a repo with no panes")
	}
}

func TestCreateWorkspaceAndTab(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"workspace create --cwd /src/repo --label se --no-focus":                                 `{"result":{"type":"workspace_created","workspace":{"workspace_id":"w11"},"tab":{"tab_id":"w11:t1"},"root_pane":{"pane_id":"w11:p1"}}}`,
		"tab create --workspace w11 --cwd /src/repo/.worktrees/se-1 --label se-1 Fix --no-focus": `{"result":{"type":"tab_created","tab":{"tab_id":"w11:t2"},"root_pane":{"pane_id":"w11:p2"}}}`,
	}}
	h := CLI{Exec: f.run}
	ws, err := h.CreateWorkspace(context.Background(), "/src/repo", "se")
	if err != nil || ws != "w11" {
		t.Fatalf("ws = %q, err = %v", ws, err)
	}
	pane, err := h.CreateTab(context.Background(), "w11", "/src/repo/.worktrees/se-1", "se-1 Fix")
	if err != nil || pane != "w11:p2" {
		t.Fatalf("pane = %q, err = %v", pane, err)
	}
}

func TestPaneForSession(t *testing.T) {
	f := &fakeRun{out: map[string]string{"agent list": `{"result":{"type":"agent_list","agents":[
{"pane_id":"w1:p1","agent_session":{"agent":"opencode","value":"ses_x"}},
{"pane_id":"w2:p1","agent_session":null}]}}`}}
	h := CLI{Exec: f.run}
	pane, ok, err := h.PaneForSession(context.Background(), "ses_x")
	if err != nil || !ok || pane != "w1:p1" {
		t.Fatalf("pane = %q, ok = %v, err = %v", pane, ok, err)
	}
	if _, ok, _ := h.PaneForSession(context.Background(), "ses_none"); ok {
		t.Fatal("matched a missing session")
	}
}

func TestRunAndFocusArgs(t *testing.T) {
	f := &fakeRun{out: map[string]string{}}
	h := CLI{Exec: f.run}
	_ = h.Run(context.Background(), "w1:p2", "opencode", "--session", "ses_x")
	_ = h.Focus(context.Background(), "w1:p2")
	if strings.Join(f.calls, "|") != "pane run w1:p2 opencode --session ses_x|agent focus w1:p2" {
		t.Fatalf("calls = %v", f.calls)
	}
	if len(f.args[0]) != 4 {
		t.Fatalf("pane run call args count = %d, want 4", len(f.args[0]))
	}
	if f.args[0][3] != "opencode --session ses_x" {
		t.Fatalf("pane run call last arg = %q, want %q", f.args[0][3], "opencode --session ses_x")
	}
}

func TestErrorEnvelope(t *testing.T) {
	f := &fakeRun{out: map[string]string{"pane list": `{"id":"x","error":{"message":"server not running"}}`}}
	if _, _, err := (CLI{Exec: f.run}).WorkspaceFor(context.Background(), "/r"); err == nil || !strings.Contains(err.Error(), "server not running") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorEnvelopeOnCommandFailure(t *testing.T) {
	origErr := fmt.Errorf("exit status 1")
	failRun := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		k := strings.Join(args, " ")
		if k == "pane list" {
			return []byte(`{"id":"x","error":{"code":"server_not_running","message":"server not running"}}`), origErr
		}
		if k == "pane run w1:p2 opencode" {
			return []byte(`{"id":"y","error":{"code":"pane_not_found","message":"pane not found"}}`), origErr
		}
		if k == "agent focus w1:p2" {
			return []byte(`{"id":"z","error":{"code":"focus_failed","message":"focus failed"}}`), origErr
		}
		return nil, nil
	}
	h := CLI{Exec: failRun}

	// Test call-based method (WorkspaceFor)
	_, _, err := h.WorkspaceFor(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "server not running") {
		t.Fatalf("WorkspaceFor err = %v, want to contain 'server not running'", err)
	}
	if !errors.Is(err, origErr) {
		t.Fatalf("WorkspaceFor err does not wrap original error")
	}

	// Test Run method
	err = h.Run(context.Background(), "w1:p2", "opencode")
	if err == nil || !strings.Contains(err.Error(), "pane not found") {
		t.Fatalf("Run err = %v, want to contain 'pane not found'", err)
	}
	if !errors.Is(err, origErr) {
		t.Fatalf("Run err does not wrap original error")
	}

	// Test Focus method
	err = h.Focus(context.Background(), "w1:p2")
	if err == nil || !strings.Contains(err.Error(), "focus failed") {
		t.Fatalf("Focus err = %v, want to contain 'focus failed'", err)
	}
	if !errors.Is(err, origErr) {
		t.Fatalf("Focus err does not wrap original error")
	}
}

func TestRunning(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"running", "status: running\npid: 1\n", nil, true},
		{"stopped", "status: stopped\n", nil, false},
		{"exit error", "status: running\n", errors.New("exit 1"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			h := CLI{Exec: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				got = args
				return []byte(tc.out), tc.err
			}}
			if h.Running(context.Background()) != tc.want {
				t.Fatalf("Running = %v, want %v", !tc.want, tc.want)
			}
			if strings.Join(got, " ") != "status server" {
				t.Fatalf("args = %v", got)
			}
		})
	}
}

func TestAgents(t *testing.T) {
	f := &fakeRun{out: map[string]string{"agent list": `{"id":"cli:agent:list","result":{"agents":[
{"agent":"opencode","agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode","value":"ses_a"},"agent_status":"idle","cwd":"/src/repo","pane_id":"w1:p1"},
{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"c-1"},"agent_status":"working","cwd":"/src/repo","pane_id":"w1:p2"},
{"agent":"opencode","agent_status":"idle","cwd":"/src/repo","pane_id":"w1:p3"}]}}`}}
	got, err := CLI{Exec: f.run}.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{PaneID: "w1:p1", Cwd: "/src/repo", SessionID: "ses_a", Source: "herdr:opencode", Status: "idle"},
		{PaneID: "w1:p2", Cwd: "/src/repo", SessionID: "c-1", Source: "herdr:claude", Status: "working"},
		{PaneID: "w1:p3", Cwd: "/src/repo", Status: "idle"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("agents = %+v, want %+v", got, want)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		path, root string
		want       bool
	}{
		{"/src/repo", "/src/repo", true},
		{"/src/repo/.worktrees/x", "/src/repo", true},
		{"/src/repo/", "/src/repo", true},
		{"/src/repository", "/src/repo", false},
		{"/src", "/src/repo", false},
	} {
		if got := Within(c.path, c.root); got != c.want {
			t.Errorf("Within(%q, %q) = %v", c.path, c.root, got)
		}
	}
}
