package btplugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
)

type fakeLive struct {
	mu        sync.Mutex
	snaps     map[string]status.Snapshot
	parents   map[string]string
	chans     chan chan opencode.Event
	snapCalls map[string]int
}

func newFakeLive() *fakeLive {
	return &fakeLive{
		snaps:     map[string]status.Snapshot{},
		parents:   map[string]string{},
		chans:     make(chan chan opencode.Event, 8),
		snapCalls: map[string]int{},
	}
}

func (f *fakeLive) Active(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }

func (f *fakeLive) Snapshot(_ context.Context, id string, _ map[string]bool) (status.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapCalls[id]++
	s, ok := f.snaps[id]
	if !ok {
		return status.Snapshot{}, errors.New("missing")
	}
	return s, nil
}

func (f *fakeLive) calls(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapCalls[id]
}

func (f *fakeLive) Root(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.parents[id]; ok {
		return p, nil
	}
	return id, nil
}

func (f *fakeLive) Subscribe(context.Context) (<-chan opencode.Event, error) {
	ch := make(chan opencode.Event, 16)
	f.chans <- ch
	return ch, nil
}

type loopUpdate struct {
	snaps     map[string]*status.Snapshot
	reachable bool
	full      bool
}

type loopHarness struct {
	loop    *Loop
	updates chan loopUpdate
	cancel  context.CancelFunc
	done    chan struct{}
}

func startLoop(t *testing.T, connect func(context.Context) (Live, error), sessions []string) *loopHarness {
	t.Helper()
	h := &loopHarness{updates: make(chan loopUpdate, 32), done: make(chan struct{})}
	h.loop = &Loop{
		Connect:  connect,
		Sessions: func() []string { return sessions },
		Update: func(s map[string]*status.Snapshot, r, f bool) {
			h.updates <- loopUpdate{s, r, f}
		},
		Debounce: 20 * time.Millisecond,
		Backoff:  []time.Duration{5 * time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.loop.Run(ctx); close(h.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not stop")
		}
	})
	return h
}

func (h *loopHarness) next(t *testing.T) loopUpdate {
	t.Helper()
	select {
	case u := <-h.updates:
		return u
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
		return loopUpdate{}
	}
}

func (h *loopHarness) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case u := <-h.updates:
		t.Fatalf("unexpected update %+v", u)
	case <-time.After(d):
	}
}

func nextChan(t *testing.T, f *fakeLive) chan opencode.Event {
	t.Helper()
	select {
	case ch := <-f.chans:
		return ch
	case <-time.After(2 * time.Second):
		t.Fatal("no subscribe")
		return nil
	}
}

func constLive(f *fakeLive) func(context.Context) (Live, error) {
	return func(context.Context) (Live, error) { return f, nil }
}

func TestLoopFullReloadOnStart(t *testing.T) {
	f := newFakeLive()
	f.snaps["a"] = status.Snapshot{}
	h := startLoop(t, constLive(f), []string{"a", "gone"})
	u := h.next(t)
	if !u.reachable || !u.full || len(u.snaps) != 2 || u.snaps["a"] == nil {
		t.Fatalf("loopUpdate = %+v", u)
	}
	if v, ok := u.snaps["gone"]; !ok || v != nil {
		t.Fatalf("failed session should be a nil entry: %+v", u.snaps)
	}
}

func TestLoopDebouncesChildEventsToRoot(t *testing.T) {
	f := newFakeLive()
	f.snaps["root"] = status.Snapshot{}
	f.parents["child"] = "root"
	h := startLoop(t, constLive(f), nil)
	h.next(t)
	ch := nextChan(t, f)
	for i := 0; i < 5; i++ {
		ch <- opencode.Event{Type: "session.status", SessionID: "child"}
	}
	ch <- opencode.Event{Type: "message.part.updated", SessionID: "child"}
	u := h.next(t)
	if u.full || !u.reachable || u.snaps["root"] == nil || len(u.snaps) != 1 {
		t.Fatalf("loopUpdate = %+v", u)
	}
	h.none(t, 80*time.Millisecond)
	if n := f.calls("root"); n != 1 {
		t.Fatalf("root loaded %d times, want 1", n)
	}
}

func TestLoopReconnectsAfterClose(t *testing.T) {
	f := newFakeLive()
	f.snaps["a"] = status.Snapshot{}
	h := startLoop(t, constLive(f), []string{"a"})
	h.next(t)
	close(nextChan(t, f))
	u := h.next(t)
	if !u.full || !u.reachable || u.snaps["a"] == nil {
		t.Fatalf("loopUpdate = %+v", u)
	}
	nextChan(t, f)
}

func TestLoopConnectFailsThenSucceeds(t *testing.T) {
	f := newFakeLive()
	f.snaps["a"] = status.Snapshot{}
	var mu sync.Mutex
	n := 0
	h := startLoop(t, func(context.Context) (Live, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n <= 2 {
			return nil, errors.New("down")
		}
		return f, nil
	}, []string{"a"})
	for i := 0; i < 2; i++ {
		if u := h.next(t); u.reachable || !u.full || u.snaps != nil {
			t.Fatalf("loopUpdate %d = %+v", i, u)
		}
	}
	if u := h.next(t); !u.reachable || !u.full || u.snaps["a"] == nil {
		t.Fatalf("loopUpdate = %+v", u)
	}
}

func TestLoopRefreshLoadsNewSession(t *testing.T) {
	f := newFakeLive()
	h := startLoop(t, constLive(f), nil)
	h.next(t)
	nextChan(t, f)
	f.mu.Lock()
	f.snaps["new"] = status.Snapshot{}
	f.mu.Unlock()
	h.loop.Refresh("new")
	u := h.next(t)
	if u.full || !u.reachable || u.snaps["new"] == nil {
		t.Fatalf("loopUpdate = %+v", u)
	}
}
