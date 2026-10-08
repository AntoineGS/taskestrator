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

func newLoopHarness(connect func(context.Context) (Live, error), sessions []string) *loopHarness {
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
	return h
}

func (h *loopHarness) run(t *testing.T) {
	t.Helper()
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
}

func startLoop(t *testing.T, connect func(context.Context) (Live, error), sessions []string) *loopHarness {
	t.Helper()
	h := newLoopHarness(connect, sessions)
	h.run(t)
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

type flakyActive struct {
	*fakeLive
	mu    sync.Mutex
	calls int
}

func (f *flakyActive) Active(ctx context.Context) (map[string]bool, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n == 1 {
		return nil, errors.New("restarting")
	}
	return f.fakeLive.Active(ctx)
}

func TestLoopRetriesWhenInitialReloadFails(t *testing.T) {
	f := &flakyActive{fakeLive: newFakeLive()}
	f.snaps["a"] = status.Snapshot{}
	h := startLoop(t, func(context.Context) (Live, error) { return f, nil }, []string{"a"})
	if u := h.next(t); u.reachable || !u.full || u.snaps != nil {
		t.Fatalf("first update = %+v", u)
	}
	if u := h.next(t); !u.reachable || !u.full || u.snaps["a"] == nil {
		t.Fatalf("second update = %+v", u)
	}
}

type blockingActive struct{ *fakeLive }

func (b blockingActive) Active(ctx context.Context) (map[string]bool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLoopNoUpdateWhenCancelledMidLoad(t *testing.T) {
	f := blockingActive{newFakeLive()}
	h := startLoop(t, func(context.Context) (Live, error) { return f, nil }, []string{"a"})
	nextChan(t, f.fakeLive)
	h.cancel()
	<-h.done
	h.none(t, 30*time.Millisecond)
}

func TestLoopActiveTimeoutIsFailure(t *testing.T) {
	f := blockingActive{newFakeLive()}
	h := newLoopHarness(func(context.Context) (Live, error) { return f, nil }, []string{"a"})
	h.loop.timeout = 10 * time.Millisecond
	h.run(t)
	if u := h.next(t); u.reachable || !u.full || u.snaps != nil {
		t.Fatalf("update = %+v", u)
	}
}

type slowSnap struct{ *fakeLive }

func (s slowSnap) Snapshot(ctx context.Context, id string, a map[string]bool) (status.Snapshot, error) {
	select {
	case <-time.After(30 * time.Millisecond):
	case <-ctx.Done():
		return status.Snapshot{}, ctx.Err()
	}
	return s.fakeLive.Snapshot(ctx, id, a)
}

func TestLoopSnapshotsHaveOwnTimeout(t *testing.T) {
	f := slowSnap{newFakeLive()}
	for _, id := range []string{"a", "b", "c", "d"} {
		f.snaps[id] = status.Snapshot{}
	}
	h := newLoopHarness(func(context.Context) (Live, error) { return f, nil }, []string{"a", "b", "c", "d"})
	h.loop.timeout = 80 * time.Millisecond
	h.run(t)
	u := h.next(t)
	for id, s := range u.snaps {
		if s == nil {
			t.Fatalf("%s timed out on a shared budget: %+v", id, u.snaps)
		}
	}
}
