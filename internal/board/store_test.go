package board

import (
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

var (
	t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	se = discover.Project{Name: "se", Repo: "/se"}
	md = discover.Project{Name: "md", Repo: "/md"}
)

func task(id, st, session string, updated time.Time) tracker.Task {
	t := tracker.Task{ID: id, Title: id, Status: st, UpdatedAt: updated}
	if session != "" {
		t.Metadata = map[string]any{tracker.MetaSession: session}
	}
	return t
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newStore() (*Store, *clock) {
	c := &clock{now: t0}
	return NewStore(c.Now), c
}

func ids(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Task.ID)
	}
	return out
}

func TestRowsGroupedAndFiltered(t *testing.T) {
	s, _ := newStore()
	closed := t0.Add(-time.Hour)
	old := t0.Add(-48 * time.Hour)
	doneRecent := task("se-done", "closed", "ses_d", closed)
	doneRecent.ClosedAt = &closed
	doneOld := task("se-old", "closed", "ses_o", old)
	doneOld.ClosedAt = &old
	s.SetTasks(se, []tracker.Task{
		task("se-run", "in_progress", "ses_run", t0.Add(-5*time.Minute)),
		task("se-ask", "in_progress", "ses_ask", t0.Add(-time.Minute)),
		task("se-rev", "review", "ses_rev", t0.Add(-time.Hour)),
		task("se-q", "open", "", t0),
		task("se-manual", "in_progress", "", t0),
		doneRecent, doneOld,
	})
	s.SetTasks(md, []tracker.Task{task("md-idle", "in_progress", "ses_idle", t0.Add(-10*time.Minute))})
	s.SetReachable(true)
	s.SetSnapshot("ses_run", &status.Snapshot{Exists: true, Running: true})
	s.SetSnapshot("ses_ask", &status.Snapshot{Exists: true, Running: true, Forms: 1})
	s.SetSnapshot("ses_idle", &status.Snapshot{Exists: true})

	got := ids(s.Rows(false))
	want := []string{"md-idle", "se-ask", "se-run", "se-rev"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	all := ids(s.Rows(true))
	if len(all) != 6 || all[4] != "se-q" || all[5] != "se-done" {
		t.Fatalf("all rows = %v", all)
	}
	if q := s.Queued(); q["se"] != 1 || q["md"] != 0 {
		t.Fatalf("queued = %v", q)
	}
}

func TestUnreachableMakesDispatchedUnknown(t *testing.T) {
	s, _ := newStore()
	s.SetTasks(se, []tracker.Task{task("se-1", "in_progress", "ses_1", t0)})
	s.SetSnapshot("ses_1", &status.Snapshot{Exists: true, Running: true})
	s.SetReachable(false)
	if r := s.Rows(false); len(r) != 1 || r[0].Display.State != status.Unknown {
		t.Fatalf("rows = %+v", r)
	}
	s.SetReachable(true)
	if r := s.Rows(false); r[0].Display.State != status.InProgress {
		t.Fatalf("rows = %+v", r)
	}
}

func TestSinceTracksStateChanges(t *testing.T) {
	s, c := newStore()
	s.SetReachable(true)
	s.SetTasks(se, []tracker.Task{task("se-1", "in_progress", "ses_1", t0.Add(-time.Hour))})
	s.SetSnapshot("ses_1", &status.Snapshot{Exists: true, Running: true})
	c.now = t0.Add(time.Minute)
	s.SetSnapshot("ses_1", &status.Snapshot{Exists: true, Running: true})
	if since := s.Rows(false)[0].Since; !since.Equal(t0) {
		t.Fatalf("an unchanged state moved Since to %v", since)
	}
	c.now = t0.Add(2 * time.Minute)
	s.SetSnapshot("ses_1", &status.Snapshot{Exists: true})
	if since := s.Rows(false)[0].Since; !since.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("Since = %v", since)
	}
	s.SetTasks(se, []tracker.Task{task("se-1", "in_progress", "ses_1", t0)})
	if since := s.Rows(false)[0].Since; !since.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("a reload reset Since to %v", since)
	}
}

func TestSessionsAndLookup(t *testing.T) {
	s, _ := newStore()
	s.SetTasks(se, []tracker.Task{
		task("se-1", "in_progress", "ses_1", t0),
		task("se-2", "review", "ses_2", t0),
		task("se-3", "closed", "ses_3", t0),
		task("se-4", "open", "", t0),
	})
	s.SetTasks(md, []tracker.Task{task("md-1", "in_progress", "ses_m", t0)})
	if got := s.Sessions(); len(got) != 2 || got[0] != "ses_1" || got[1] != "ses_m" {
		t.Fatalf("Sessions = %v", got)
	}
	if got := s.SessionsFor("/se"); len(got) != 1 || got[0] != "ses_1" {
		t.Fatalf("SessionsFor = %v", got)
	}
	if p, ok := s.ProjectForSession("ses_m"); !ok || p.Name != "md" {
		t.Fatalf("ProjectForSession = %v, %v", p, ok)
	}
	if _, ok := s.ProjectForSession("ses_x"); ok {
		t.Fatal("found an unknown session")
	}
}

func TestSetTasksReplacesProjectTasks(t *testing.T) {
	s, _ := newStore()
	s.SetReachable(true)
	s.SetTasks(se, []tracker.Task{task("se-1", "in_progress", "ses_1", t0)})
	s.SetTasks(se, nil)
	if r := s.Rows(true); len(r) != 0 {
		t.Fatalf("rows = %v", ids(r))
	}
}
