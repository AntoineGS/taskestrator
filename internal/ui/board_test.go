package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

var (
	now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	se  = discover.Project{Name: "se", Repo: "/se"}
	md  = discover.Project{Name: "md", Repo: "/md"}
)

func dispatched(id, title, st, session string) tracker.Task {
	return tracker.Task{ID: id, Title: title, Status: st, UpdatedAt: now.Add(-3 * time.Minute),
		Metadata: map[string]any{tracker.MetaSession: session, tracker.MetaWorktree: "/se/.worktrees/" + id}}
}

func newTestBoard(t *testing.T) (*Board, *fakeTracker, *fakeLive, *jumpRecorder) {
	t.Helper()
	tr := &fakeTracker{tasks: map[string][]tracker.Task{
		"/se": {
			dispatched("se-1", "Publish snapshot", "in_progress", "ses_1"),
			dispatched("se-2", "Decode text", "in_progress", "ses_2"),
			{ID: "se-3", Title: "Queued work", Status: "open"},
		},
		"/md": {dispatched("md-1", "Stream results", "review", "ses_m")},
	}}
	lv := &fakeLive{
		active: map[string]bool{"ses_1": true},
		snaps: map[string]status.Snapshot{
			"ses_1": {Exists: true, Running: true},
			"ses_2": {Exists: true, Forms: 1},
		},
		parents: map[string]string{"ses_kid": "ses_1"},
	}
	j := &jumpRecorder{}
	b := NewBoard(context.Background(), BoardConfig{
		Projects: []discover.Project{md, se}, Tracker: tr, Live: lv, Jump: j.jump,
		Reread: 10 * time.Second, Now: func() time.Time { return now }, After: noAfter,
	})
	return b, tr, lv, j
}

func TestBoardShowsGroupedTasks(t *testing.T) {
	b, _, _, _ := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	v := view(m)
	for _, want := range []string{"WAITING ON YOU", "se-2", "question", "IN PROGRESS", "se-1", "REVIEW", "md-1", "queued: se 1"} {
		if !strings.Contains(v, want) {
			t.Errorf("view is missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "se-3") {
		t.Errorf("queued task shown without 'a':\n%s", v)
	}
	if strings.Index(v, "se-2") > strings.Index(v, "se-1 ") {
		t.Errorf("the waiting task should come first:\n%s", v)
	}
}

func TestBoardToggleAllAndFilter(t *testing.T) {
	b, _, _, _ := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	m, _ = press(t, m, "a")
	if !strings.Contains(view(m), "se-3") {
		t.Fatalf("'a' did not show queued tasks:\n%s", view(m))
	}
	m, _ = press(t, m, "/", "d", "e", "c", "enter")
	v := view(m)
	if !strings.Contains(v, "se-2") || strings.Contains(v, "se-1 ") || strings.Contains(v, "md-1") {
		t.Fatalf("filter 'dec' not applied:\n%s", v)
	}
	m, _ = press(t, m, "/", "esc")
	if !strings.Contains(view(m), "md-1") {
		t.Fatalf("esc did not clear the filter:\n%s", view(m))
	}
}

func TestBoardEnterJumpsAndQuits(t *testing.T) {
	b, _, _, j := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	_, msgs := press(t, m, "j", "enter")
	if len(j.targets) != 1 || j.targets[0].TaskID != "se-1" || j.targets[0].SessionID != "ses_1" || j.targets[0].Repo != "/se" {
		t.Fatalf("targets = %+v", j.targets)
	}
	if !quits(msgs) {
		t.Fatal("the board did not quit after jumping")
	}
}

func TestBoardWithoutHerdrExplains(t *testing.T) {
	b, _, _, _ := newTestBoard(t)
	b.jump = nil
	m, _ := settle(t, b, b.Init())
	m, msgs := press(t, m, "enter")
	if quits(msgs) || !strings.Contains(view(m), "herdr") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestBoardUnreachableShowsBanner(t *testing.T) {
	b, _, lv, _ := newTestBoard(t)
	lv.set(func(f *fakeLive) { f.down = true })
	m, _ := settle(t, b, b.Init())
	v := view(m)
	if !strings.Contains(v, "OpenCode") || !strings.Contains(v, "UNKNOWN") {
		t.Fatalf("view:\n%s", v)
	}
	if !strings.Contains(v, "md-1") {
		t.Fatalf("the review task should still show:\n%s", v)
	}
}

func TestBoardChildEventRefreshesRootSession(t *testing.T) {
	b, _, lv, _ := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	before := lv.calls("ses_1")
	lv.set(func(f *fakeLive) { f.snaps["ses_1"] = status.Snapshot{Exists: true, Running: true, Permissions: 1} })
	m, _ = settle(t, m, func() tea.Msg {
		return eventMsg{ev: opencode.Event{Type: "permission.asked", SessionID: "ses_kid"}, ok: true}
	})
	if lv.calls("ses_1") <= before {
		t.Fatal("a child event did not refresh the task's session")
	}
	if !strings.Contains(view(m), "permission") {
		t.Fatalf("view:\n%s", view(m))
	}
}

func TestBoardSettledEventRereadsProject(t *testing.T) {
	b, tr, _, _ := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	before := tr.listCount("/se")
	settle(t, m, func() tea.Msg {
		return eventMsg{ev: opencode.Event{Type: "session.execution.succeeded", SessionID: "ses_1"}, ok: true}
	})
	if tr.listCount("/se") <= before {
		t.Fatal("a settled event did not re-read the project's tasks")
	}
}

func TestBoardIgnoresIrrelevantEvents(t *testing.T) {
	b, _, lv, _ := newTestBoard(t)
	m, _ := settle(t, b, b.Init())
	before := lv.calls("ses_1")
	settle(t, m, func() tea.Msg {
		return eventMsg{ev: opencode.Event{Type: "session.text.delta", SessionID: "ses_1"}, ok: true}
	})
	if lv.calls("ses_1") != before {
		t.Fatal("an irrelevant event triggered a refresh")
	}
}

func TestBoardReconnectsAndReloads(t *testing.T) {
	b, _, lv, _ := newTestBoard(t)
	var delays []time.Duration
	b.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		if _, ok := msg.(reconnectMsg); ok {
			delays = append(delays, d)
			return func() tea.Msg { return msg }
		}
		return nil
	}
	m, _ := settle(t, b, b.Init())
	before := lv.calls("ses_1")
	m, _ = settle(t, m, func() tea.Msg { return eventMsg{ok: false} })
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("reconnect delays = %v", delays)
	}
	if n := lv.subscribeCount(); n != 2 {
		t.Fatalf("subscribes = %d", n)
	}
	if lv.calls("ses_1") <= before {
		t.Fatal("reconnecting did not reload snapshots")
	}
	if strings.Contains(view(m), "disconnected") {
		t.Fatalf("the banner stayed after reconnecting:\n%s", view(m))
	}
}

func TestBoardQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		b, _, _, _ := newTestBoard(t)
		m, _ := settle(t, b, b.Init())
		if _, msgs := press(t, m, k); !quits(msgs) {
			t.Errorf("%s did not quit", k)
		}
	}
}

func TestAge(t *testing.T) {
	cases := map[time.Duration]string{30 * time.Second: "now", 12 * time.Minute: "12m", 5 * time.Hour: "5h", 50 * time.Hour: "2d"}
	for d, want := range cases {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
