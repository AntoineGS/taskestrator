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
	reviewCheckTimeout = 30 * time.Second
)

var logger = log.New(os.Stderr, "tsk: ", 0)

// Server is tsk's side of the bt plugin protocol. Handle is the Peer handler.
type Server struct {
	Peer         *Peer
	Engine       *Engine
	Loop         *Loop
	Actions      *Actions
	Roots        []string // from initialize options.roots; default ~/gits
	Scan         func(roots []string) []discover.Project
	HerdrRunning func() bool
	Tracker      tracker.Tracker
	Stop         func() // ends the process on shutdown; may be nil

	// mu serializes "engine call + sending its result" and guards herdrUp
	// and down.
	mu      sync.Mutex
	herdrUp bool
	down    bool

	startOnce sync.Once

	scanOnce sync.Once
	repoMu   sync.RWMutex
	byDB     map[string]string

	reviewMu sync.Mutex
	reviewed map[string]bool
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
	s.Roots = nil
	if raw, ok := p.Options["roots"].([]any); ok {
		for _, r := range raw {
			if str, ok := r.(string); ok && str != "" {
				s.Roots = append(s.Roots, config.ExpandHome(str))
			}
		}
	}
	if len(s.Roots) == 0 {
		s.Roots = []string{config.ExpandHome("~/gits")}
	}
	s.startOnce.Do(func() {
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
		byDB := map[string]string{}
		for _, p := range scan(s.Roots) {
			if _, dup := byDB[p.Name]; !dup {
				byDB[p.Name] = p.Repo
			}
		}
		s.repoMu.Lock()
		s.byDB = byDB
		s.repoMu.Unlock()
	})
}

func (s *Server) toast(msg, tone string) {
	if err := s.Peer.Notify("ui.toast", Toast{Message: msg, Tone: tone}); err != nil {
		logger.Printf("send toast: %v", err)
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

// checkReviewStatus warns once per database lacking the review status. It
// runs in the background so slow bd calls never block the reader.
func (s *Server) checkReviewStatus(ctx context.Context, beads []Bead) {
	if s.Tracker == nil {
		return
	}
	todo := map[string]string{}
	s.reviewMu.Lock()
	if s.reviewed == nil {
		s.reviewed = map[string]bool{}
	}
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

	for db, repo := range todo {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, reviewCheckTimeout)
			defer cancel()
			has, err := s.Tracker.HasReviewStatus(cctx, repo)
			if err != nil {
				logger.Printf("review status check for %s: %v", db, err)
				s.reviewMu.Lock()
				delete(s.reviewed, db)
				s.reviewMu.Unlock()
				return
			}
			if !has {
				s.toast(fmt.Sprintf(`%s has no "review" status; run in %s: bd config set status.custom "review:wip"`, db, repo), "warn")
			}
		}()
	}
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
		s.down = true
		s.toast(unreachableMessage, "error")
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
	srv := &Server{Engine: NewEngine(time.Now), Loop: loop, Tracker: bd, Stop: cancel}

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
		Late:         func(t Toast) { srv.toast(t.Message, t.Tone) },
	}
	srv.Peer = NewPeer(in, out, srv.Handle)
	err := srv.Peer.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
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
