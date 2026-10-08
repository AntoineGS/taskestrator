package btplugin

import (
	"context"
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
type Loop struct {
	Connect  func(ctx context.Context) (Live, error)
	Sessions func() []string
	Update   func(snaps map[string]*status.Snapshot, reachable, full bool)

	Debounce time.Duration
	Backoff  []time.Duration

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
		attempt = 0
		if !l.serve(ctx, live, ch) {
			return
		}
		// Stream closed: back off, then reconnect.
		if !wait(l.backoff(0)) {
			return
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

func (l *Loop) load(ctx context.Context, live Live, ids []string, full bool) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	active, err := live.Active(ctx)
	if err != nil {
		if ctx.Err() == nil || full {
			l.Update(nil, false, true)
		}
		return
	}
	snaps := make(map[string]*status.Snapshot, len(ids))
	for _, id := range ids {
		snap, err := live.Snapshot(ctx, id, active)
		if err != nil {
			snaps[id] = nil
			continue
		}
		snaps[id] = &snap
	}
	l.Update(snaps, true, full)
}

// serve handles one connected stream. It returns false when ctx is done and
// true when the stream closed.
func (l *Loop) serve(ctx context.Context, live Live, ch <-chan opencode.Event) bool {
	l.mu.Lock()
	l.pending = nil
	l.mu.Unlock()
	l.load(ctx, live, l.Sessions(), true)

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
			return false
		case ev, ok := <-ch:
			if !ok {
				return ctx.Err() == nil
			}
			if ev.SessionID == "" || !opencode.Relevant(ev.Type) {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
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
