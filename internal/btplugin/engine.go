package btplugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Env is the per-call environment the engine needs to decide on actions.
type Env struct {
	HerdrUp bool
	RepoFor func(b Bead) string // resolved checkout ("" unknown): b.Repo or a roots scan by DB
}

type sinceEntry struct {
	value string
	at    time.Time
}

// Engine maps beads and session snapshots to the plugin state pushed to bt.
// It is safe for concurrent use, with these caller obligations:
//   - Pushed state is recorded at compute time, so callers must serialize
//     "engine call + sending its result" under one mutex of their own;
//     otherwise results can reach bt out of order.
//   - Env.RepoFor is called while the engine mutex is held. It must be fast
//     (memoized) and must never call back into the Engine.
//   - The result of All must always be sent as a replace push, even when it is
//     empty.
type Engine struct {
	now func() time.Time

	mu        sync.Mutex
	beads     map[BeadKey]Bead
	snaps     map[string]*status.Snapshot // present = load attempted; nil = failed
	seen      map[string]bool             // live sessions already reported as new
	reachable bool
	since     map[BeadKey]sinceEntry
	pushed    map[BeadKey][]byte
}

func NewEngine(now func() time.Time) *Engine {
	return &Engine{
		now:       now,
		beads:     map[BeadKey]Bead{},
		snaps:     map[string]*status.Snapshot{},
		seen:      map[string]bool{},
		reachable: true,
		since:     map[BeadKey]sinceEntry{},
		pushed:    map[BeadKey][]byte{},
	}
}

func metaString(b Bead, key string) string {
	raw, ok := b.Metadata[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func keyOf(b Bead) BeadKey { return BeadKey{DB: b.DB, ID: b.ID} }

func (e *Engine) display(b Bead) status.Display {
	session := metaString(b, tracker.MetaSession)
	var snap *status.Snapshot
	if e.reachable && session != "" {
		snap = e.snaps[session]
	}
	return status.Derive(b.Status, session, snap)
}

// state computes the bead's state; ok is false when it must not be shown.
func (e *Engine) state(b Bead, env Env) (BeadState, bool) {
	k := keyOf(b)
	d := e.display(b)
	var value string
	var actions []string
	jump := func() {
		if env.HerdrUp {
			actions = []string{"jump"}
		}
	}
	dispatch := func() {
		if env.HerdrUp && env.RepoFor != nil && env.RepoFor(b) != "" {
			actions = []string{"dispatch"}
		}
	}
	switch d.State {
	case status.Review:
		value = "review"
	case status.Queued:
		value = "queued"
		dispatch()
	case status.Lost:
		value = "lost"
		dispatch()
	case status.Waiting:
		value = "waiting"
		jump()
	case status.InProgress:
		value = "running"
		jump()
	case status.Unknown:
		value = "unknown"
		jump()
	default:
		delete(e.since, k)
		return BeadState{}, false
	}

	s, seen := e.since[k]
	if !seen || s.value != value {
		s = sinceEntry{value: value, at: e.now()}
		e.since[k] = s
	}
	at := s.at
	st := BeadState{
		DB: b.DB, ID: b.ID,
		Fields:  map[string]FieldState{"state": {Value: value, Since: &at}},
		Actions: actions,
	}
	if sec := e.section(b, d); sec != "" {
		st.Sections = map[string]string{"agent": sec}
	}
	return st, true
}

func (e *Engine) section(b Bead, d status.Display) string {
	session := metaString(b, tracker.MetaSession)
	if session == "" {
		return ""
	}
	lines := []string{fmt.Sprintf("**Session** `%s`", session)}
	if w := metaString(b, tracker.MetaWorktree); w != "" {
		lines = append(lines, fmt.Sprintf("**Worktree** `%s`", w))
	}
	if br := metaString(b, tracker.MetaBranch); br != "" {
		lines = append(lines, fmt.Sprintf("**Branch** `%s`", br))
	}
	out := strings.Join(lines, "\n") + "\n\n"
	switch d.State {
	case status.Waiting:
		out += "Waiting on you: " + d.Detail
		if d.Detail == "question" {
			if snap := e.snaps[session]; snap != nil && snap.Question != "" {
				out += " — " + snap.Question
			}
		}
	case status.Lost:
		out += "Session not found in OpenCode; dispatch again with D."
	case status.Unknown:
		out += "OpenCode service unreachable."
	default:
		out = strings.TrimRight(out, "\n")
	}
	return out
}

// pending reports a live bead whose session snapshot has never been loaded
// while the service is believed reachable. Nothing is pushed for it, so bt
// keeps showing the previous value (or nothing).
func (e *Engine) pending(b Bead) bool {
	if !e.reachable {
		return false
	}
	s := e.liveSession(b)
	if s == "" {
		return false
	}
	_, loaded := e.snaps[s]
	return !loaded
}

// diff computes pushes for every bead against what was last pushed.
func (e *Engine) diff(env Env) (set []BeadState, clear []BeadKey) {
	keys := make([]BeadKey, 0, len(e.beads))
	for k := range e.beads {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].DB != keys[j].DB {
			return keys[i].DB < keys[j].DB
		}
		return keys[i].ID < keys[j].ID
	})
	for _, k := range keys {
		if e.pending(e.beads[k]) {
			continue
		}
		st, ok := e.state(e.beads[k], env)
		if !ok {
			if _, was := e.pushed[k]; was {
				delete(e.pushed, k)
				clear = append(clear, k)
			}
			continue
		}
		enc, _ := json.Marshal(st)
		if bytes.Equal(e.pushed[k], enc) {
			continue
		}
		e.pushed[k] = enc
		set = append(set, st)
	}
	return set, clear
}

// Sync replaces the bead set. It returns the pushes needed and the session IDs
// that appeared (to load snapshots for).
func (e *Engine) Sync(beads []Bead, env Env) (set []BeadState, clear []BeadKey, newSessions []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	next := make(map[BeadKey]Bead, len(beads))
	for _, b := range beads {
		next[keyOf(b)] = b
	}
	var gone []BeadKey
	for k := range e.beads {
		if _, ok := next[k]; !ok {
			gone = append(gone, k)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].DB != gone[j].DB {
			return gone[i].DB < gone[j].DB
		}
		return gone[i].ID < gone[j].ID
	})
	for _, k := range gone {
		delete(e.since, k)
		if _, was := e.pushed[k]; was {
			delete(e.pushed, k)
			clear = append(clear, k)
		}
	}

	live := map[string]bool{}
	e.beads = next
	for _, b := range beads {
		if s := e.liveSession(b); s != "" {
			live[s] = true
			if !e.seen[s] {
				e.seen[s] = true
				newSessions = append(newSessions, s)
			}
		}
	}
	for s := range e.snaps {
		if !live[s] {
			delete(e.snaps, s)
		}
	}
	for s := range e.seen {
		if !live[s] {
			delete(e.seen, s)
		}
	}
	sort.Strings(newSessions)

	set, c := e.diff(env)
	return set, append(clear, c...), newSessions
}

func (e *Engine) liveSession(b Bead) string {
	if b.Status == "closed" || b.Status == "review" {
		return ""
	}
	return metaString(b, tracker.MetaSession)
}

// SetSnapshots records snapshots (nil value = failed to load). reachable=false
// marks every session unknown. It returns the pushes needed.
func (e *Engine) SetSnapshots(snaps map[string]*status.Snapshot, reachable bool, env Env) (set []BeadState, clear []BeadKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reachable = reachable
	for s, snap := range snaps {
		e.snaps[s] = snap
	}
	return e.diff(env)
}

// All returns the full current state for a replace push and records it as
// pushed.
func (e *Engine) All(env Env) []BeadState {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pushed = map[BeadKey][]byte{}
	set, _ := e.diff(env)
	return set
}

// Sessions lists the sessions of live beads.
func (e *Engine) Sessions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, b := range e.beads {
		if s := e.liveSession(b); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) Bead(k BeadKey) (Bead, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.beads[k]
	return b, ok
}

func (e *Engine) Display(k BeadKey) (status.Display, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.beads[k]
	if !ok {
		return status.Display{}, false
	}
	return e.display(b), true
}

func (e *Engine) Reachable() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reachable
}
