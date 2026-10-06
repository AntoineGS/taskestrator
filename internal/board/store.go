// Package board holds the board's in-memory view of tasks and their live state.
package board

import (
	"sort"
	"time"

	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Row is one task as the board shows it.
type Row struct {
	Project discover.Project
	Task    tracker.Task
	Display status.Display
	Since   time.Time
}

type key struct{ repo, id string }

type entry struct {
	project discover.Project
	task    tracker.Task
	display status.Display
	since   time.Time
}

// Store combines tasks from every project with session snapshots. It is not
// safe for concurrent use; the Bubble Tea model owns it.
type Store struct {
	now       func() time.Time
	reachable bool
	entries   map[key]*entry
	snaps     map[string]*status.Snapshot
}

// NewStore returns an empty store that uses now for timestamps.
func NewStore(now func() time.Time) *Store {
	return &Store{now: now, entries: map[key]*entry{}, snaps: map[string]*status.Snapshot{}}
}

func (s *Store) derive(t tracker.Task) status.Display {
	sid := t.Meta(tracker.MetaSession)
	var snap *status.Snapshot
	if s.reachable {
		snap = s.snaps[sid]
	}
	return status.Derive(t.Status, sid, snap)
}

// SetTasks replaces one project's tasks.
func (s *Store) SetTasks(p discover.Project, tasks []tracker.Task) {
	old := map[string]*entry{}
	for k, e := range s.entries {
		if k.repo == p.Repo {
			old[k.id] = e
			delete(s.entries, k)
		}
	}
	for _, t := range tasks {
		e := &entry{project: p, task: t, display: s.derive(t)}
		switch o, ok := old[t.ID]; {
		case ok && o.display == e.display:
			e.since = o.since
		case ok:
			e.since = s.now()
		default:
			e.since = t.UpdatedAt
		}
		s.entries[key{p.Repo, t.ID}] = e
	}
}

func (s *Store) recompute() {
	for _, e := range s.entries {
		if d := s.derive(e.task); d != e.display {
			e.display = d
			e.since = s.now()
		}
	}
}

// SetSnapshot records a session's live state; nil means it could not be loaded.
func (s *Store) SetSnapshot(sessionID string, snap *status.Snapshot) {
	s.snaps[sessionID] = snap
	s.recompute()
}

// SetReachable records whether the OpenCode service answered.
func (s *Store) SetReachable(ok bool) {
	s.reachable = ok
	s.recompute()
}

// Reachable reports whether the OpenCode service last answered.
func (s *Store) Reachable() bool { return s.reachable }

var order = map[status.State]int{
	status.Waiting: 0, status.InProgress: 1, status.Review: 2, status.Lost: 3,
	status.Unknown: 4, status.Queued: 5, status.Done: 6,
}

// Rows returns dispatched, non-closed tasks grouped by state. With showAll it
// adds queued tasks and tasks closed in the last 24 hours.
func (s *Store) Rows(showAll bool) []Row {
	cutoff := s.now().Add(-24 * time.Hour)
	var rows []Row
	for _, e := range s.entries {
		switch e.display.State {
		case status.Untracked:
			continue
		case status.Done:
			if !showAll || e.task.ClosedAt == nil || e.task.ClosedAt.Before(cutoff) {
				continue
			}
		case status.Queued:
			if !showAll {
				continue
			}
		default:
			if e.task.Meta(tracker.MetaSession) == "" {
				continue
			}
		}
		rows = append(rows, Row{Project: e.project, Task: e.task, Display: e.display, Since: e.since})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if order[a.Display.State] != order[b.Display.State] {
			return order[a.Display.State] < order[b.Display.State]
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		if a.Project.Name != b.Project.Name {
			return a.Project.Name < b.Project.Name
		}
		return a.Task.ID < b.Task.ID
	})
	return rows
}

// Queued counts queued tasks per project name.
func (s *Store) Queued() map[string]int {
	counts := map[string]int{}
	for _, e := range s.entries {
		if e.display.State == status.Queued {
			counts[e.project.Name]++
		}
	}
	return counts
}

func live(t tracker.Task) bool {
	return t.Meta(tracker.MetaSession) != "" && t.Status != "closed" && t.Status != tracker.StatusReview
}

// Sessions returns the session IDs of dispatched tasks that are neither closed nor in review.
func (s *Store) Sessions() []string { return s.sessions("") }

// SessionsFor is Sessions limited to one repository.
func (s *Store) SessionsFor(repo string) []string { return s.sessions(repo) }

func (s *Store) sessions(repo string) []string {
	var out []string
	for k, e := range s.entries {
		if (repo == "" || k.repo == repo) && live(e.task) {
			out = append(out, e.task.Meta(tracker.MetaSession))
		}
	}
	sort.Strings(out)
	return out
}

// ProjectForSession returns the project of the task dispatched with sessionID.
func (s *Store) ProjectForSession(sessionID string) (discover.Project, bool) {
	for _, e := range s.entries {
		if e.task.Meta(tracker.MetaSession) == sessionID {
			return e.project, true
		}
	}
	return discover.Project{}, false
}
