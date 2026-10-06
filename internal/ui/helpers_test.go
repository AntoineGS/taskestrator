package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// collect runs cmd and returns the messages it produces, expanding batches.
// Commands that block (event waits) are dropped after a short timeout.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range b {
				out = append(out, collect(c)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// settle feeds every resulting message back into the model until none remain.
func settle(t *testing.T, m tea.Model, cmd tea.Cmd) (tea.Model, []tea.Msg) {
	t.Helper()
	var seen []tea.Msg
	queue := collect(cmd)
	for i := 0; len(queue) > 0; i++ {
		if i > 500 {
			t.Fatal("model did not settle")
		}
		msg := queue[0]
		queue = queue[1:]
		seen = append(seen, msg)
		var c tea.Cmd
		m, c = m.Update(msg)
		queue = append(queue, collect(c)...)
	}
	return m, seen
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func press(t *testing.T, m tea.Model, keys ...string) (tea.Model, []tea.Msg) {
	t.Helper()
	var all []tea.Msg
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(key(k))
		var seen []tea.Msg
		m, seen = settle(t, m, cmd)
		all = append(all, seen...)
	}
	return m, all
}

func view(m tea.Model) string { return m.View().Content }

func noAfter(time.Duration, tea.Msg) tea.Cmd { return nil }

func quits(msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

type fakeTracker struct {
	mu        sync.Mutex
	tasks     map[string][]tracker.Task
	ready     map[string][]tracker.Task
	lists     map[string]int
	hasReview bool
	added     bool
}

func (f *fakeTracker) Ready(_ context.Context, repo string) ([]tracker.Task, error) {
	return f.ready[repo], nil
}
func (f *fakeTracker) List(_ context.Context, repo string) ([]tracker.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists == nil {
		f.lists = map[string]int{}
	}
	f.lists[repo]++
	return f.tasks[repo], nil
}
func (f *fakeTracker) Claim(context.Context, string, string) error   { return nil }
func (f *fakeTracker) Unclaim(context.Context, string, string) error { return nil }
func (f *fakeTracker) SetMetadata(context.Context, string, string, map[string]string) error {
	return nil
}
func (f *fakeTracker) UnsetMetadata(context.Context, string, string, ...string) error { return nil }
func (f *fakeTracker) HasReviewStatus(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasReview, nil
}
func (f *fakeTracker) AddReviewStatus(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added, f.hasReview = true, true
	return nil
}

func (f *fakeTracker) listCount(repo string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists[repo]
}

type fakeLive struct {
	mu         sync.Mutex
	snaps      map[string]status.Snapshot
	active     map[string]bool
	parents    map[string]string
	down       bool
	subscribes int
	snapCalls  map[string]int
}

func (f *fakeLive) Active(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	return f.active, nil
}
func (f *fakeLive) Snapshot(_ context.Context, id string, _ map[string]bool) (status.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapCalls == nil {
		f.snapCalls = map[string]int{}
	}
	f.snapCalls[id]++
	return f.snaps[id], nil
}
func (f *fakeLive) Root(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.parents[id] != "" {
		id = f.parents[id]
	}
	return id, nil
}
func (f *fakeLive) Subscribe(context.Context) (<-chan opencode.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribes++
	if f.down {
		return nil, errors.New("connection refused")
	}
	return make(chan opencode.Event), nil
}

func (f *fakeLive) set(fn func(*fakeLive)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeLive) calls(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapCalls[id]
}

func (f *fakeLive) subscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribes
}

type jumpRecorder struct {
	mu      sync.Mutex
	targets []dispatch.Target
	err     error
}

func (j *jumpRecorder) jump(_ context.Context, t dispatch.Target) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.targets = append(j.targets, t)
	return j.err
}
