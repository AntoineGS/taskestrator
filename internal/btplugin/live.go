package btplugin

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
)

const requestTimeout = 10 * time.Second

// Live is the part of the OpenCode client the loop uses.
type Live interface {
	Active(ctx context.Context) (map[string]bool, error)
	Snapshot(ctx context.Context, id string, active map[string]bool) (status.Snapshot, error)
	Root(ctx context.Context, id string) (string, error)
	Subscribe(ctx context.Context) (<-chan opencode.Event, error)
}

// Loop follows the OpenCode event stream and reports session snapshots
// through Update. Update is only ever called from the goroutine running Run.
//
// Update(nil, false, true) may repeat on every failed connect or reload
// attempt, so consumers must treat it idempotently. Refresh ids still pending
// when a stream (re)connects are dropped because the full reload covers
// Sessions(); callers must make a session visible in Sessions() before
// calling Refresh. Stream events for root sessions not in Sessions() are
// ignored.
type Loop struct {
	Connect  func(ctx context.Context) (Live, error)
	Sessions func() []string
	Update   func(snaps map[string]*status.Snapshot, reachable, full bool)

	Debounce time.Duration
	Backoff  []time.Duration

	timeout time.Duration // per request; requestTimeout when zero

	mu      sync.Mutex
	pending []string
	wake    chan struct{}
	once    sync.Once
}

var defaultBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

func (l *Loop) init() { l.once.Do(func() { l.wake = make(chan struct{}, 1) }) }

// Refresh asks the loop to reload these sessions soon. It never blocks.
func (l *Loop) Refresh(ids ...string) {
	if len(ids) == 0 {
		return
	}
	l.init()
	l.mu.Lock()
	l.pending = append(l.pending, ids...)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Loop) reqTimeout() time.Duration {
	if l.timeout > 0 {
		return l.timeout
	}
	return requestTimeout
}

func (l *Loop) debounce() time.Duration {
	if l.Debounce > 0 {
		return l.Debounce
	}
	return 300 * time.Millisecond
}

func (l *Loop) backoff(i int) time.Duration {
	b := l.Backoff
	if len(b) == 0 {
		b = defaultBackoff
	}
	if i >= len(b) {
		i = len(b) - 1
	}
	return b[i]
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Run follows the stream until ctx is done.
func (l *Loop) Run(ctx context.Context) {
	l.init()
	attempt := 0
	wait := func(d time.Duration) bool { return sleep(ctx, d) }
	for ctx.Err() == nil {
		live, ch, err := l.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			l.Update(nil, false, true)
			if !wait(l.backoff(attempt)) {
				return
			}
			attempt++
			continue
		}
		switch l.serve(ctx, live, ch) {
		case serveStopped:
			return
		case serveFailed:
			if !wait(l.backoff(attempt)) {
				return
			}
			attempt++
		default:
			// Stream closed: back off, then reconnect.
			attempt = 0
			if !wait(l.backoff(0)) {
				return
			}
		}
	}
}

func (l *Loop) connect(ctx context.Context) (Live, <-chan opencode.Event, error) {
	live, err := l.Connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	ch, err := live.Subscribe(ctx)
	if err != nil {
		return nil, nil, err
	}
	return live, ch, nil
}

// load fetches the live state of ids and reports it. It returns false when
// the service could not be reached (Update has been told, unless ctx is done).
func (l *Loop) load(ctx context.Context, live Live, ids []string, full bool) bool {
	actx, cancel := context.WithTimeout(ctx, l.reqTimeout())
	active, err := live.Active(actx)
	cancel()
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		l.Update(nil, false, true)
		return false
	}
	snaps := make(map[string]*status.Snapshot, len(ids))
	for _, id := range ids {
		sctx, cancel := context.WithTimeout(ctx, l.reqTimeout())
		snap, err := live.Snapshot(sctx, id, active)
		cancel()
		if err != nil {
			snaps[id] = nil
			continue
		}
		snaps[id] = &snap
	}
	if ctx.Err() != nil {
		return false
	}
	l.Update(snaps, true, full)
	return true
}

type serveResult int

const (
	serveStopped serveResult = iota
	serveClosed
	serveFailed
)

// serve handles one connected stream. It returns serveStopped when ctx is
// done, serveClosed when the stream closed and serveFailed when the initial
// reload could not reach the service.
func (l *Loop) serve(ctx context.Context, live Live, ch <-chan opencode.Event) serveResult {
	l.mu.Lock()
	l.pending = nil
	l.mu.Unlock()
	if !l.load(ctx, live, l.Sessions(), true) {
		if ctx.Err() != nil {
			return serveStopped
		}
		return serveFailed
	}

	fire := make(chan string, 64)
	timers := map[string]*time.Timer{}
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return serveStopped
		case ev, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return serveStopped
				}
				return serveClosed
			}
			if ev.SessionID == "" || !opencode.Relevant(ev.Type) {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, l.reqTimeout())
			root, err := live.Root(rctx, ev.SessionID)
			cancel()
			if err != nil {
				continue
			}
			if _, ok := timers[root]; ok {
				continue
			}
			timers[root] = time.AfterFunc(l.debounce(), func() {
				select {
				case fire <- root:
				case <-ctx.Done():
				}
			})
		case root := <-fire:
			delete(timers, root)
			if !slices.Contains(l.Sessions(), root) {
				continue
			}
			l.load(ctx, live, []string{root}, false)
		case <-l.wake:
			l.mu.Lock()
			ids := l.pending
			l.pending = nil
			l.mu.Unlock()
			if len(ids) > 0 {
				l.load(ctx, live, ids, false)
			}
		}
	}
}
