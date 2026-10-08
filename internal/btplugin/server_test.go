package btplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/status"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

type sent struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (s *syncBuf) sent() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sent
	for _, line := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if line == "" {
			continue
		}
		var m sent
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			panic(err)
		}
		out = append(out, m)
	}
	return out
}

func (s *syncBuf) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog sends the package logger to a buffer for the test.
func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	logger.SetOutput(buf)
	t.Cleanup(func() { logger.SetOutput(os.Stderr) })
	return buf
}

func (s *syncBuf) methods() []string {
	var out []string
	for _, m := range s.sent() {
		out = append(out, m.Method)
	}
	return out
}

func (s *syncBuf) toasts() []Toast {
	var out []Toast
	for _, m := range s.sent() {
		if m.Method == "ui.toast" {
			var t Toast
			_ = json.Unmarshal(m.Params, &t)
			out = append(out, t)
		}
	}
	return out
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

type serverRig struct {
	srv   *Server
	out   *syncBuf
	rec   *recorder
	probe *atomic.Int32
	scans *atomic.Int32
	loop  chan struct{}
}

func newRig(t *testing.T) *serverRig {
	t.Helper()
	r := &serverRig{out: &syncBuf{}, rec: &recorder{}, probe: &atomic.Int32{}, scans: &atomic.Int32{}, loop: make(chan struct{}, 4)}
	r.srv = &Server{
		toastGap: time.Nanosecond,
		Engine:   NewEngine(time.Now),
		Loop: &Loop{Connect: func(ctx context.Context) (Live, error) {
			r.loop <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		Tracker: r.rec,
		HerdrRunning: func() bool {
			r.probe.Add(1)
			return true
		},
		Scan: func([]string) []discover.Project {
			r.scans.Add(1)
			return []discover.Project{{Name: "scanned", Repo: "/repos/scanned"}}
		},
	}
	r.srv.Peer = NewPeer(strings.NewReader(""), r.out, r.srv.Handle)
	return r
}

func ptr(s string) *string { return &s }

func syncParams(t *testing.T, beads ...Bead) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(SyncParams{Revision: 1, Beads: beads})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func queued(db, id string, repo *string) Bead {
	return Bead{DB: db, ID: id, Repo: repo, Status: "open", Title: id}
}

func TestInitializeReturnsManifestAndStartsLoop(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := r.srv.Handle(ctx, "initialize", json.RawMessage(`{"protocolVersion":1,"options":{"roots":["/a","/b"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(PluginManifest)
	if !ok || m.Name != "tsk" || m.ProtocolVersion != 1 {
		t.Fatalf("result = %#v", got)
	}
	if strings.Join(r.srv.Roots, ",") != "/a,/b" {
		t.Fatalf("roots = %v", r.srv.Roots)
	}
	select {
	case <-r.loop:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not start")
	}
}

func TestInitializeDefaultsRoots(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := r.srv.Handle(ctx, "initialize", json.RawMessage(`{"protocolVersion":1}`)); err != nil {
		t.Fatal(err)
	}
	if len(r.srv.Roots) != 1 || !strings.HasSuffix(r.srv.Roots[0], "/gits") {
		t.Fatalf("roots = %v", r.srv.Roots)
	}
}

func TestSyncPushesSetThenClear(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	repo := ptr("/repos/a")
	if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("a", "a-1", repo))); err != nil {
		t.Fatal(err)
	}
	if got := r.out.methods(); strings.Join(got, ",") != "state.set" {
		t.Fatalf("methods = %v", got)
	}
	var set StateSetParams
	_ = json.Unmarshal(r.out.sent()[0].Params, &set)
	if set.Replace || len(set.Beads) != 1 || set.Beads[0].Fields["state"].Value != "queued" ||
		len(set.Beads[0].Actions) != 1 || set.Beads[0].Actions[0] != "dispatch" {
		t.Fatalf("set = %+v", set)
	}

	// a-1 vanishes while a-2 appears: set comes before clear.
	if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("a", "a-2", repo))); err != nil {
		t.Fatal(err)
	}
	if got := r.out.methods(); strings.Join(got, ",") != "state.set,state.set,state.clear" {
		t.Fatalf("methods = %v", got)
	}
	// An unchanged sync pushes nothing.
	if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("a", "a-2", repo))); err != nil {
		t.Fatal(err)
	}
	if n := len(r.out.methods()); n != 3 {
		t.Fatalf("messages = %v", r.out.methods())
	}
}

func TestSyncProbesHerdrOncePerSync(t *testing.T) {
	r := newRig(t)
	repo := ptr("/repos/a")
	beads := []Bead{queued("a", "a-1", repo), queued("a", "a-2", repo), queued("a", "a-3", repo)}
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, beads...)); err != nil {
		t.Fatal(err)
	}
	if n := r.probe.Load(); n != 1 {
		t.Fatalf("probes = %d", n)
	}
}

func TestSyncResolvesRepoByScanOnce(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	p := syncParams(t, queued("scanned", "s-1", nil), queued("unknown", "u-1", nil))
	if _, err := r.srv.Handle(ctx, "beads.sync", p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("scanned", "s-2", nil))); err != nil {
		t.Fatal(err)
	}
	if n := r.scans.Load(); n != 1 {
		t.Fatalf("scans = %d", n)
	}
	var set StateSetParams
	_ = json.Unmarshal(r.out.sent()[0].Params, &set)
	actions := map[string]int{}
	for _, b := range set.Beads {
		actions[b.ID] = len(b.Actions)
	}
	if actions["s-1"] != 1 || actions["u-1"] != 0 {
		t.Fatalf("actions = %v", actions)
	}
}

func TestReviewStatusToastOncePerDB(t *testing.T) {
	r := newRig(t)
	r.rec.noReview = true
	ctx := context.Background()
	repo := ptr("/repos/a")
	for range 2 {
		if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("a", "a-1", repo), queued("a", "a-2", repo))); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return len(r.out.toasts()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	toasts := r.out.toasts()
	want := `1 project lacks the "review" status (a); run in each: bd config set status.custom "review:wip"`
	if len(toasts) != 1 || toasts[0].Message != want || toasts[0].Tone != "warn" {
		t.Fatalf("toasts = %+v", toasts)
	}
}

// countingTracker records how many HasReviewStatus calls run at once.
type countingTracker struct {
	*recorder
	mu        sync.Mutex
	cur, peak int
}

func (c *countingTracker) HasReviewStatus(context.Context, string) (bool, error) {
	c.mu.Lock()
	c.cur++
	c.peak = max(c.peak, c.cur)
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	c.cur--
	c.mu.Unlock()
	return false, nil
}

func TestReviewStatusOneCombinedToastAndBoundedChecks(t *testing.T) {
	r := newRig(t)
	ct := &countingTracker{recorder: r.rec}
	r.srv.Tracker = ct
	var beads []Bead
	for i := range 7 {
		db := "d" + string(rune('0'+i))
		beads = append(beads, queued(db, db+"-1", ptr("/repos/"+db)))
	}
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, beads...)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(r.out.toasts()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	toasts := r.out.toasts()
	want := `7 projects lack the "review" status (d0, d1, d2, d3, d4 and 2 more); run in each: bd config set status.custom "review:wip"`
	if len(toasts) != 1 || toasts[0].Message != want || toasts[0].Tone != "warn" {
		t.Fatalf("toasts = %+v", toasts)
	}
	ct.mu.Lock()
	peak := ct.peak
	ct.mu.Unlock()
	if peak > 4 {
		t.Fatalf("%d review checks ran at once, want at most 4", peak)
	}
}

func TestReviewToastWaitsAfterUnreachableToast(t *testing.T) {
	r := newRig(t)
	r.srv.toastGap = 200 * time.Millisecond
	r.rec.noReview = true
	r.srv.onUpdate(nil, false, true)
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, queued("a", "a-1", ptr("/repos/a")))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if n := len(r.out.toasts()); n != 1 {
		t.Fatalf("review toast not delayed: %+v", r.out.toasts())
	}
	eventually(t, func() bool { return len(r.out.toasts()) == 2 })
	toasts := r.out.toasts()
	if toasts[0].Message != unreachableMessage || !strings.Contains(toasts[1].Message, `lacks the "review" status`) {
		t.Fatalf("toasts = %+v", toasts)
	}
}

func TestUnreachableToastRetriedWhenTooSoon(t *testing.T) {
	r := newRig(t)
	r.srv.toastGap = 100 * time.Millisecond
	r.rec.noReview = true
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, queued("a", "a-1", ptr("/repos/a")))); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(r.out.toasts()) == 1 })
	r.srv.onUpdate(nil, false, true) // bt would drop it: not sent, not latched
	if n := len(r.out.toasts()); n != 1 {
		t.Fatalf("toasts = %+v", r.out.toasts())
	}
	time.Sleep(120 * time.Millisecond)
	r.srv.onUpdate(nil, false, true)
	toasts := r.out.toasts()
	if len(toasts) != 2 || toasts[1].Message != unreachableMessage {
		t.Fatalf("toasts = %+v", toasts)
	}
}

func TestHerdrProbeTickerPushesChange(t *testing.T) {
	r := newRig(t)
	var up atomic.Bool
	up.Store(true)
	r.srv.HerdrRunning = func() bool { return up.Load() }
	r.srv.herdrEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := r.srv.Handle(ctx, "initialize", json.RawMessage(`{"protocolVersion":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("a", "a-1", ptr("/repos/a")))); err != nil {
		t.Fatal(err)
	}
	up.Store(false)
	eventually(t, func() bool {
		msgs := r.out.sent()
		if len(msgs) < 2 || msgs[len(msgs)-1].Method != "state.set" {
			return false
		}
		last := msgs[len(msgs)-1]
		var set StateSetParams
		_ = json.Unmarshal(last.Params, &set)
		return len(set.Beads) == 1 && len(set.Beads[0].Actions) == 0
	})
	n := len(r.out.sent())
	time.Sleep(50 * time.Millisecond)
	if len(r.out.sent()) != n {
		t.Fatalf("unchanged probe pushed again: %v", r.out.methods())
	}
}

func TestInitializeRootsRaceFree(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.srv.Handle(ctx, "initialize", json.RawMessage(`{"protocolVersion":1,"options":{"roots":["/a"]}}`))
	}()
	_, _ = r.srv.Handle(ctx, "beads.sync", syncParams(t, queued("scanned", "s-1", nil)))
	<-done
}

// serveRig runs a Server with real actions over a pipe.
func serveRig(t *testing.T, drainWait time.Duration) (*actionsHarness, *io.PipeWriter, chan error) {
	t.Helper()
	h := newActions(t, t.TempDir(), mkBead("a", "open", ""))
	h.r.block = make(chan struct{})
	h.r.entered = make(chan struct{}, 1)
	srv := &Server{Engine: h.a.Engine, Actions: h.a, drainWait: drainWait}
	in, w := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), in, out) }()
	t.Cleanup(func() { _ = w.Close() })
	return h, w, done
}

const invokeLine = `{"jsonrpc":"2.0","id":1,"method":"action.invoke","params":{"action":"dispatch","bead":{"db":"db","id":"a"}}}` + "\n"

func TestServeReturnsOnEOF(t *testing.T) {
	srv := &Server{Engine: NewEngine(time.Now)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), strings.NewReader(""), &syncBuf{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
}

func TestServeShutdownNotificationStops(t *testing.T) {
	_, w, done := serveRig(t, 0)
	if _, err := io.WriteString(w, `{"jsonrpc":"2.0","method":"shutdown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop on shutdown")
	}
}

func TestServeWaitsForInflightAction(t *testing.T) {
	h, w, done := serveRig(t, 2*time.Second)
	if _, err := io.WriteString(w, invokeLine); err != nil {
		t.Fatal(err)
	}
	<-h.r.entered
	_ = w.Close()
	select {
	case <-done:
		t.Fatal("Serve returned with a dispatch in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(h.r.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the dispatch finished")
	}
	if !slices.Contains(h.r.calls(), "claim a") {
		t.Fatalf("dispatch did not finish before Serve returned: %v", h.r.calls())
	}
}

func TestServeDrainIsBounded(t *testing.T) {
	h, w, done := serveRig(t, 50*time.Millisecond)
	logs := captureLog(t)
	t.Cleanup(func() { close(h.r.block) })
	if _, err := io.WriteString(w, invokeLine); err != nil {
		t.Fatal(err)
	}
	<-h.r.entered
	_ = w.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve waited past its bound")
	}
	if want := "tsk: dispatch a interrupted; check the bead, worktree and session"; !strings.Contains(logs.text(), want) {
		t.Fatalf("log = %q", logs.text())
	}
}

func TestReviewStatusNoToastWhenPresent(t *testing.T) {
	r := newRig(t)
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, queued("a", "a-1", ptr("/repos/a")))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if toasts := r.out.toasts(); len(toasts) != 0 {
		t.Fatalf("toasts = %+v", toasts)
	}
}

func TestUnreachableToastOncePerTransition(t *testing.T) {
	r := newRig(t)
	r.srv.onUpdate(nil, false, true)
	r.srv.onUpdate(nil, false, true)
	if toasts := r.out.toasts(); len(toasts) != 1 || toasts[0].Message != unreachableMessage || toasts[0].Tone != "error" {
		t.Fatalf("toasts = %+v", toasts)
	}
	r.srv.onUpdate(map[string]*status.Snapshot{}, true, true)
	r.srv.onUpdate(nil, false, true)
	if toasts := r.out.toasts(); len(toasts) != 2 {
		t.Fatalf("toasts = %+v", toasts)
	}
}

func TestFullUpdatePushesReplaceEvenWhenEmpty(t *testing.T) {
	r := newRig(t)
	r.srv.onUpdate(map[string]*status.Snapshot{}, true, true)
	msgs := r.out.sent()
	if len(msgs) != 1 || msgs[0].Method != "state.set" {
		t.Fatalf("messages = %v", r.out.methods())
	}
	var set StateSetParams
	_ = json.Unmarshal(msgs[0].Params, &set)
	if !set.Replace || set.Beads == nil || len(set.Beads) != 0 {
		t.Fatalf("set = %+v (%s)", set, msgs[0].Params)
	}
	if !strings.Contains(string(msgs[0].Params), `"beads":[]`) {
		t.Fatalf("params = %s", msgs[0].Params)
	}
}

func TestPartialUpdatePushesOnlyChanges(t *testing.T) {
	r := newRig(t)
	repo := ptr("/repos/a")
	b := queued("a", "a-1", repo)
	b.Status = "in_progress"
	b.Metadata = map[string]json.RawMessage{"tsk.session": json.RawMessage(`"ses_1"`)}
	if _, err := r.srv.Handle(context.Background(), "beads.sync", syncParams(t, b)); err != nil {
		t.Fatal(err)
	}
	if n := len(r.out.methods()); n != 0 {
		t.Fatalf("pending session pushed: %v", r.out.methods())
	}
	r.srv.onUpdate(map[string]*status.Snapshot{"ses_1": {Exists: true, Running: true}}, true, false)
	msgs := r.out.sent()
	if len(msgs) != 1 || msgs[0].Method != "state.set" {
		t.Fatalf("messages = %v", r.out.methods())
	}
	var set StateSetParams
	_ = json.Unmarshal(msgs[0].Params, &set)
	if set.Replace || len(set.Beads) != 1 || set.Beads[0].Fields["state"].Value != "running" {
		t.Fatalf("set = %+v", set)
	}
}

func TestShutdownStops(t *testing.T) {
	r := newRig(t)
	stopped := false
	r.srv.Stop = func() { stopped = true }
	if _, err := r.srv.Handle(context.Background(), "shutdown", nil); err != nil || !stopped {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
}

func TestUnknownMethod(t *testing.T) {
	r := newRig(t)
	_, err := r.srv.Handle(context.Background(), "nope", nil)
	var re *RPCError
	if err == nil || !asRPC(err, &re) || re.Code != -32601 {
		t.Fatalf("err = %v", err)
	}
}

func asRPC(err error, target **RPCError) bool {
	re, ok := err.(*RPCError)
	if ok {
		*target = re
	}
	return ok
}
