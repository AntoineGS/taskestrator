package btplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/execx"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

const (
	unreachableMessage = "OpenCode service unreachable; start it with: opencode service start"
	herdrProbeTimeout  = 5 * time.Second
	herdrProbeEvery    = 30 * time.Second
	reviewCheckTimeout = 30 * time.Second
	reviewCheckLimit   = 4 // concurrent bd processes
	reviewListLimit    = 5 // project names in the review toast

	// defaultToastGap keeps toasts apart: bt drops a plugin toast arriving
	// within 1s of the previous one.
	defaultToastGap = 1100 * time.Millisecond
	// defaultDrainWait bounds the wait for in-flight actions on exit, inside
	// the 2s bt allows between closing stdin and killing the plugin.
	defaultDrainWait = 1500 * time.Millisecond
)

var logger = log.New(os.Stderr, "tsk: ", 0)

// Server is tsk's side of the bt plugin protocol. Handle is the Peer handler.
type Server struct {
	Peer         *Peer
	Engine       *Engine
	Loop         *Loop
	Actions      *Actions
	Roots        []string // from initialize options.roots; default ~/gits; guarded by mu
	Scan         func(roots []string) []discover.Project
	HerdrRunning func() bool
	Tracker      tracker.Tracker
	Stop         func() // ends Serve on shutdown; Serve sets it

	// mu serializes "engine call + sending its result" and guards herdrUp,
	// down, Roots and model.
	mu      sync.Mutex
	herdrUp bool
	down    bool
	model   *opencode.ModelRef // from initialize options.model; nil uses OpenCode's default

	startOnce sync.Once

	scanOnce sync.Once
	repoMu   sync.RWMutex
	byDB     map[string]string

	reviewMu  sync.Mutex
	reviewed  map[string]bool
	reviewSem chan struct{}

	toastMu   sync.Mutex
	lastToast time.Time

	invokeMu   sync.Mutex
	invokes    sync.WaitGroup
	draining   bool
	running    map[int]InvokeParams
	nextInvoke int

	toastGap   time.Duration // defaultToastGap when zero
	herdrEvery time.Duration // herdrProbeEvery when zero
	drainWait  time.Duration // defaultDrainWait when zero
}

// Serve runs the protocol on in and out until stdin closes or bt sends
// shutdown. It then cancels in-flight actions and waits a bounded time for
// them, so an interrupted dispatch can undo its completed steps before bt
// kills the process.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.Stop = cancel
	s.Peer = NewPeer(in, out, s.Handle)
	err := s.Peer.Run(ctx)
	cancel()
	s.drain()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// track registers an action invoke; it refuses once Serve is draining.
func (s *Server) track(p InvokeParams) (done func(), ok bool) {
	s.invokeMu.Lock()
	defer s.invokeMu.Unlock()
	if s.draining {
		return nil, false
	}
	if s.running == nil {
		s.running = map[int]InvokeParams{}
	}
	s.nextInvoke++
	n := s.nextInvoke
	s.running[n] = p
	s.invokes.Add(1)
	return func() {
		s.invokeMu.Lock()
		delete(s.running, n)
		s.invokeMu.Unlock()
		s.invokes.Done()
	}, true
}

// drain waits up to drainWait for tracked invokes and logs each dispatch
// still running after that, since its undo may not finish.
func (s *Server) drain() {
	s.invokeMu.Lock()
	s.draining = true
	s.invokeMu.Unlock()

	wait := s.drainWait
	if wait <= 0 {
		wait = defaultDrainWait
	}
	done := make(chan struct{})
	go func() {
		s.invokes.Wait()
		close(done)
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return
	case <-t.C:
	}

	s.invokeMu.Lock()
	var ids []string
	for _, p := range s.running {
		if p.Action == "dispatch" {
			ids = append(ids, p.Bead.ID)
		}
	}
	s.invokeMu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		logger.Printf("dispatch %s interrupted; check the bead, worktree and session", id)
	}
}

func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.initialize(ctx, params)
	case "beads.sync":
		var p SyncParams
		if err := json.Unmarshal(params, &p); err != nil {
			logger.Printf("beads.sync: %v", err)
			return nil, nil
		}
		s.sync(ctx, p.Beads)
		return nil, nil
	case "action.invoke":
		var p InvokeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, Fail("bad_params", err.Error())
		}
		if s.Actions == nil {
			return nil, Fail("unavailable", "actions are not available")
		}
		done, ok := s.track(p)
		if !ok {
			return nil, Fail("unavailable", "tsk is shutting down")
		}
		defer done()
		return s.Actions.Invoke(ctx, p)
	case "shutdown":
		if s.Stop != nil {
			s.Stop()
		}
		return nil, nil
	}
	return nil, &RPCError{Code: -32601, Message: "method not found: " + method}
}

func (s *Server) initialize(ctx context.Context, params json.RawMessage) (any, error) {
	var p InitializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, Fail("bad_params", err.Error())
		}
	}
	var roots []string
	if raw, ok := p.Options["roots"].([]any); ok {
		for _, r := range raw {
			if str, ok := r.(string); ok && str != "" {
				roots = append(roots, config.ExpandHome(str))
			}
		}
	}
	if len(roots) == 0 {
		roots = []string{config.ExpandHome("~/gits")}
	}
	var model *opencode.ModelRef
	if raw, ok := p.Options["model"].(string); ok && strings.TrimSpace(raw) != "" {
		m, err := opencode.ParseModel(raw)
		if err != nil {
			logger.Printf("ignoring options.model: %v", err)
			go s.toastAfterGap(ctx, fmt.Sprintf("tsk: options.model %q is not provider/id; using OpenCode's default model", raw), "warn")
		} else {
			model = &m
		}
	}
	s.mu.Lock()
	s.Roots = roots
	s.model = model
	s.mu.Unlock()
	s.startOnce.Do(func() {
		if s.HerdrRunning != nil {
			go s.watchHerdr(ctx)
		}
		if s.Loop == nil {
			return
		}
		s.Loop.Update = s.onUpdate
		if s.Loop.Sessions == nil {
			s.Loop.Sessions = s.Engine.Sessions
		}
		go s.Loop.Run(ctx)
	})
	return Manifest(), nil
}

func (s *Server) env() Env {
	return Env{HerdrUp: s.herdrUp, RepoFor: s.repoFor}
}

// repoFor is Env.RepoFor: it only reads memoized state.
func (s *Server) repoFor(b Bead) string {
	if b.Repo != nil && *b.Repo != "" {
		return *b.Repo
	}
	s.repoMu.RLock()
	defer s.repoMu.RUnlock()
	return s.byDB[b.DB]
}

// Model returns the model new sessions use, or nil for OpenCode's default.
func (s *Server) Model() *opencode.ModelRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

// resolve scans the roots once (lazily) when some bead has no repo.
func (s *Server) resolve(beads []Bead) {
	need := false
	for _, b := range beads {
		if b.Repo == nil || *b.Repo == "" {
			need = true
			break
		}
	}
	if !need {
		return
	}
	s.scanOnce.Do(func() {
		scan := s.Scan
		if scan == nil {
			scan = func(roots []string) []discover.Project {
				projects, warns := discover.Scan(roots, nil)
				for _, w := range warns {
					logger.Printf("scan: %s", w)
				}
				return projects
			}
		}
		s.mu.Lock()
		roots := s.Roots
		s.mu.Unlock()
		byDB := map[string]string{}
		for _, p := range scan(roots) {
			if _, dup := byDB[p.Name]; !dup {
				byDB[p.Name] = p.Repo
			}
		}
		s.repoMu.Lock()
		s.byDB = byDB
		s.repoMu.Unlock()
	})
}

func (s *Server) gap() time.Duration {
	if s.toastGap > 0 {
		return s.toastGap
	}
	return defaultToastGap
}

// sendToast sends a toast and records when. The caller holds s.toastMu.
func (s *Server) sendToast(msg, tone string) {
	if err := s.Peer.Notify("ui.toast", Toast{Message: msg, Tone: tone}); err != nil {
		logger.Printf("send toast: %v", err)
	}
	s.lastToast = time.Now()
}

// toastNow sends a toast unless bt would drop it for following the previous
// one too closely. It reports whether it sent.
func (s *Server) toastNow(msg, tone string) bool {
	s.toastMu.Lock()
	defer s.toastMu.Unlock()
	if !s.lastToast.IsZero() && time.Since(s.lastToast) < s.gap() {
		return false
	}
	s.sendToast(msg, tone)
	return true
}

// toastAfterGap sends a toast once the gap since the previous one has passed.
func (s *Server) toastAfterGap(ctx context.Context, msg, tone string) {
	for {
		s.toastMu.Lock()
		wait := s.gap() - time.Since(s.lastToast)
		if s.lastToast.IsZero() || wait <= 0 {
			s.sendToast(msg, tone)
			s.toastMu.Unlock()
			return
		}
		s.toastMu.Unlock()
		if !sleep(ctx, wait) {
			return
		}
	}
}

// push sends state.set then state.clear for the given result. The caller
// holds s.mu.
func (s *Server) push(set []BeadState, clear []BeadKey) {
	if len(set) > 0 {
		if err := s.Peer.Notify("state.set", StateSetParams{Beads: set}); err != nil {
			logger.Printf("send state.set: %v", err)
		}
	}
	if len(clear) > 0 {
		if err := s.Peer.Notify("state.clear", StateClearParams{Beads: clear}); err != nil {
			logger.Printf("send state.clear: %v", err)
		}
	}
}

func (s *Server) probeHerdr() bool {
	return s.HerdrRunning != nil && s.HerdrRunning()
}

// watchHerdr re-probes herdr between syncs and pushes the action changes when
// it starts or stops.
func (s *Server) watchHerdr(ctx context.Context) {
	every := s.herdrEvery
	if every <= 0 {
		every = herdrProbeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		up := s.probeHerdr()
		s.mu.Lock()
		if up != s.herdrUp {
			s.herdrUp = up
			set, clear := s.Engine.Diff(s.env())
			s.push(set, clear)
		}
		s.mu.Unlock()
	}
}

func (s *Server) sync(ctx context.Context, beads []Bead) {
	s.resolve(beads)
	up := s.probeHerdr()

	s.mu.Lock()
	s.herdrUp = up
	set, clear, fresh := s.Engine.Sync(beads, s.env())
	s.push(set, clear)
	s.mu.Unlock()

	if s.Loop != nil {
		s.Loop.Refresh(fresh...)
	}
	s.checkReviewStatus(ctx, beads)
}

// checkReviewStatus checks each database once for the review status, running
// at most reviewCheckLimit bd processes at a time, and sends one combined
// warning for the databases lacking it. It runs in the background so slow bd
// calls never block the reader.
func (s *Server) checkReviewStatus(ctx context.Context, beads []Bead) {
	if s.Tracker == nil {
		return
	}
	todo := map[string]string{}
	s.reviewMu.Lock()
	if s.reviewed == nil {
		s.reviewed = map[string]bool{}
		s.reviewSem = make(chan struct{}, reviewCheckLimit)
	}
	sem := s.reviewSem
	for _, b := range beads {
		if _, ok := todo[b.DB]; ok || s.reviewed[b.DB] {
			continue
		}
		if repo := s.repoFor(b); repo != "" {
			s.reviewed[b.DB] = true
			todo[b.DB] = repo
		}
	}
	s.reviewMu.Unlock()
	if len(todo) == 0 {
		return
	}

	unmark := func(db string) {
		s.reviewMu.Lock()
		delete(s.reviewed, db)
		s.reviewMu.Unlock()
	}
	go func() {
		var mu sync.Mutex
		var missing []string
		var wg sync.WaitGroup
		for db, repo := range todo {
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					unmark(db)
					return
				}
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(ctx, reviewCheckTimeout)
				defer cancel()
				has, err := s.Tracker.HasReviewStatus(cctx, repo)
				if err != nil {
					logger.Printf("review status check for %s: %v", db, err)
					unmark(db)
					return
				}
				if !has {
					mu.Lock()
					missing = append(missing, db)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(missing) > 0 {
			s.toastAfterGap(ctx, reviewWarning(missing), "warn")
		}
	}()
}

func reviewWarning(dbs []string) string {
	sort.Strings(dbs)
	list := strings.Join(dbs, ", ")
	if len(dbs) > reviewListLimit {
		list = fmt.Sprintf("%s and %d more", strings.Join(dbs[:reviewListLimit], ", "), len(dbs)-reviewListLimit)
	}
	subject := fmt.Sprintf("%d projects lack", len(dbs))
	if len(dbs) == 1 {
		subject = "1 project lacks"
	}
	return fmt.Sprintf(`%s the "review" status (%s); run in each: bd config set status.custom "review:wip"`, subject, list)
}

// onUpdate is the Loop.Update callback.
func (s *Server) onUpdate(snaps map[string]*status.Snapshot, reachable, full bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, clear := s.Engine.SetSnapshots(snaps, reachable, s.env())
	if full {
		all := s.Engine.All(s.env())
		if all == nil {
			all = []BeadState{}
		}
		if err := s.Peer.Notify("state.set", StateSetParams{Replace: true, Beads: all}); err != nil {
			logger.Printf("send state.set: %v", err)
		}
	} else {
		s.push(set, clear)
	}
	switch {
	case reachable:
		s.down = false
	case !s.down:
		// Latch only once sent: a toast bt would drop is retried on the
		// next unreachable update.
		s.down = s.toastNow(unreachableMessage, "error")
	}
}

// Main runs the plugin on in and out until stdin closes or bt sends shutdown.
func Main(ctx context.Context, in io.Reader, out io.Writer) error {
	if _, err := exec.LookPath("bd"); err != nil {
		return fmt.Errorf("bd is not on PATH: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	bd := tracker.New()
	git := gitwt.New()
	loop := &Loop{Connect: connectOpenCode}
	srv := &Server{Engine: NewEngine(time.Now), Loop: loop, Tracker: bd}

	// Assign interfaces only on success, so they never hold a typed nil pointer.
	var h herdr.Herdr
	if _, err := exec.LookPath("herdr"); err == nil {
		cli := herdr.New()
		h = cli
		srv.HerdrRunning = func() bool {
			pctx, cancel := context.WithTimeout(ctx, herdrProbeTimeout)
			defer cancel()
			return cli.Running(pctx)
		}
	} else {
		logger.Printf("herdr is not on PATH; jumping and dispatch are disabled")
	}

	srv.Actions = &Actions{
		Engine:       srv.Engine,
		Tracker:      bd,
		Git:          git,
		Deps:         dispatchDeps(ctx, h, bd, git, dispatchOpenCode),
		Herdr:        h,
		HerdrUp:      srv.HerdrRunning,
		RepoFor:      srv.repoFor,
		OnDispatched: func(sessionID string) { loop.Refresh(sessionID) },
		Model:        srv.Model,
		Late:         func(t Toast) { go srv.toastAfterGap(ctx, t.Message, t.Tone) },
	}
	return srv.Serve(ctx, in, out)
}

// dispatchDeps builds Actions.Deps. It resolves the OpenCode service on every
// dispatch, so a service restart on a new port or password is picked up.
func dispatchDeps(ctx context.Context, h herdr.Herdr, bd tracker.Tracker, git gitwt.Git, connect func(context.Context) (dispatch.OpenCode, error)) func() (dispatch.Deps, error) {
	return func() (dispatch.Deps, error) {
		if h == nil {
			return dispatch.Deps{}, errors.New("herdr is not on PATH")
		}
		oc, err := connect(ctx)
		if err != nil {
			return dispatch.Deps{}, err
		}
		return dispatch.Deps{Tracker: bd, OpenCode: oc, Herdr: h, Git: git, Shell: execx.Command("sh")}, nil
	}
}

func dispatchOpenCode(ctx context.Context) (dispatch.OpenCode, error) {
	c, err := opencode.FromService(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func connectOpenCode(ctx context.Context) (Live, error) {
	c, err := opencode.FromService(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}
