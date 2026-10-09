package link

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

var at = time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)

type cliHarness struct {
	tr       *fakeTracker
	oc       *fakeOpenCode
	log      bytes.Buffer
	stderr   bytes.Buffer
	connects int
	deps     Deps
}

func newCLI() *cliHarness {
	tr, oc, _ := world()
	h := &cliHarness{tr: tr, oc: oc}
	h.deps = Deps{
		Tracker: tr,
		Connect: func(context.Context) (OpenCode, error) { h.connects++; return oc, nil },
		Log:     func() (io.WriteCloser, error) { return nopWriteCloser{&h.log}, nil },
		Now:     func() time.Time { return at },
	}
	return h
}

func (h *cliHarness) run(args ...string) int {
	return Main(context.Background(), args, &h.stderr, h.deps)
}

func TestMainBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--command", "bd update tsk-a --claim"},
		{"--session", "ses_kid"},
		{"--session", "ses_kid", "--command", "x", "extra"},
		{"--bogus"},
	} {
		h := newCLI()
		if code := h.run(args...); code != 2 {
			t.Fatalf("%v: code = %d, want 2", args, code)
		}
	}
}

func TestMainNoClaimSkipsOpenCode(t *testing.T) {
	h := newCLI()
	if code := h.run("--session", "ses_kid", "--command", "go test ./..."); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if h.connects != 0 || h.log.Len() != 0 {
		t.Fatalf("connects = %d, log = %q", h.connects, h.log.String())
	}
}

func TestMainLinksInSessionDirectory(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "")
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a --claim"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, h.stderr.String())
	}
	if !slices.Equal(h.tr.set, []string{"/r/.worktrees/x tsk-a ses_root /r/.worktrees/x"}) {
		t.Fatalf("set = %v", h.tr.set)
	}
	want := "2026-10-09T18:00:00Z ses_kid /r/.worktrees/x tsk-a linked ses_root\n"
	if h.log.String() != want {
		t.Fatalf("log = %q, want %q", h.log.String(), want)
	}
}

func TestMainWorkdir(t *testing.T) {
	for workdir, dir := range map[string]string{"sub": "/r/.worktrees/x/sub", "/elsewhere": "/elsewhere"} {
		h := newCLI()
		h.run("--session", "ses_kid", "--workdir", workdir, "--command", "bd update tsk-a --claim")
		if !slices.Equal(h.tr.shown, []string{dir + " tsk-a"}) {
			t.Fatalf("workdir %s: shown = %v, want %s", workdir, h.tr.shown, dir)
		}
	}
}

func TestMainConnectFails(t *testing.T) {
	h := newCLI()
	h.deps.Connect = func(context.Context) (OpenCode, error) { return nil, errors.New("connection refused") }
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a tsk-b --claim"); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(h.log.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "tsk-a error: connection refused") || !strings.HasSuffix(lines[1], "tsk-b error: connection refused") {
		t.Fatalf("log = %q", h.log.String())
	}
	if len(h.tr.set) != 0 {
		t.Fatalf("set = %v", h.tr.set)
	}
}

func TestMainResultsAreLogged(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "open", "")
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a fix-this --claim"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	got := h.log.String()
	if !strings.Contains(got, " tsk-a not_in_progress\n") || !strings.Contains(got, " fix-this not_found\n") {
		t.Fatalf("log = %q", got)
	}
}

func TestMainLogUnavailableFallsBackToStderr(t *testing.T) {
	h := newCLI()
	h.tr.tasks["tsk-a"] = task("tsk-a", "in_progress", "")
	h.deps.Log = func() (io.WriteCloser, error) { return nil, errors.New("read-only") }
	if code := h.run("--session", "ses_kid", "--command", "bd update tsk-a --claim"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(h.stderr.String(), "read-only") || !strings.Contains(h.stderr.String(), "tsk-a linked ses_root") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

func TestLogPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if p, err := LogPath(); err != nil || p != "/state/tsk/link.log" {
		t.Fatalf("p = %q, err = %v", p, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if p, err := LogPath(); err != nil || p != "/home/u/.local/state/tsk/link.log" {
		t.Fatalf("p = %q, err = %v", p, err)
	}
}
