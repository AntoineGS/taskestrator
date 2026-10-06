package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/board"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// BoardConfig configures NewBoard. Live and Jump are nil when OpenCode or
// herdr is unavailable.
type BoardConfig struct {
	Projects []discover.Project
	Tracker  tracker.Tracker
	Live     Live
	Jump     Jumper
	Reread   time.Duration
	Now      func() time.Time
	After    After
	Warnings []string
}

// Board is the global task board.
type Board struct {
	ctx      context.Context
	projects []discover.Project
	tracker  tracker.Tracker
	live     Live
	jump     Jumper
	reread   time.Duration
	now      func() time.Time
	after    After
	store    *board.Store

	rows     []board.Row
	cursor   int
	selected rowKey
	showAll  bool
	filter   string
	editing  bool
	warnings []string
	errs     map[string]string
	liveErr  string
	message  string
	pending  map[string]bool
	backoff  time.Duration
}

type rowKey struct{ repo, id string }

func keyOf(r board.Row) rowKey { return rowKey{r.Project.Repo, r.Task.ID} }

type tasksMsg struct {
	project discover.Project
	tasks   []tracker.Task
	err     error
}

type subscribedMsg struct {
	ch  <-chan opencode.Event
	err error
}

type eventMsg struct {
	ev opencode.Event
	ch <-chan opencode.Event
	ok bool
}

type rootMsg struct {
	root    string
	settled bool
}

type refreshMsg struct{ id string }

type reconnectMsg struct{}

// NewBoard returns the board model.
func NewBoard(ctx context.Context, cfg BoardConfig) *Board {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = realAfter
	}
	if cfg.Reread <= 0 {
		cfg.Reread = 10 * time.Second
	}
	return &Board{
		ctx: ctx, projects: cfg.Projects, tracker: cfg.Tracker, live: cfg.Live, jump: cfg.Jump,
		reread: cfg.Reread, now: cfg.Now, after: cfg.After, store: board.NewStore(cfg.Now),
		warnings: cfg.Warnings, errs: map[string]string{}, pending: map[string]bool{},
	}
}

func (b *Board) Init() tea.Cmd {
	cmds := []tea.Cmd{b.after(b.reread, tickMsg{})}
	for _, p := range b.projects {
		cmds = append(cmds, b.loadTasks(p))
	}
	if b.live != nil {
		cmds = append(cmds, b.subscribe())
	} else {
		b.liveErr = "OpenCode service unavailable"
	}
	return tea.Batch(cmds...)
}

func (b *Board) loadTasks(p discover.Project) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(b.ctx, requestTimeout)
		defer cancel()
		tasks, err := b.tracker.List(ctx, p.Repo)
		return tasksMsg{project: p, tasks: tasks, err: err}
	}
}

func (b *Board) loadSnaps(ids []string) tea.Cmd {
	return func() tea.Msg { return loadSnapshots(b.ctx, b.live, ids) }
}

func (b *Board) subscribe() tea.Cmd {
	return func() tea.Msg {
		ch, err := b.live.Subscribe(b.ctx)
		return subscribedMsg{ch: ch, err: err}
	}
}

func waitEvent(ch <-chan opencode.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return eventMsg{ev: ev, ch: ch, ok: ok}
	}
}

func (b *Board) resolveRoot(ev opencode.Event) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(b.ctx, requestTimeout)
		defer cancel()
		root, err := b.live.Root(ctx, ev.SessionID)
		if err != nil {
			return nil
		}
		return rootMsg{root: root, settled: opencode.Settled(ev.Type)}
	}
}

func (b *Board) scheduleReconnect() tea.Cmd {
	if b.backoff == 0 {
		b.backoff = time.Second
	} else if b.backoff < 30*time.Second {
		b.backoff *= 2
	}
	return b.after(b.backoff, reconnectMsg{})
}

func (b *Board) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return b.key(msg)
	case tasksMsg:
		if msg.err != nil {
			b.errs[msg.project.Name] = msg.err.Error()
			return b, nil
		}
		delete(b.errs, msg.project.Name)
		b.store.SetTasks(msg.project, msg.tasks)
		b.refreshRows()
		return b, b.loadSnaps(b.store.SessionsFor(msg.project.Repo))
	case snapsMsg:
		if msg.reachable {
			b.liveErr = ""
			for id, s := range msg.snaps {
				b.store.SetSnapshot(id, s)
			}
			b.store.SetReachable(true)
		} else {
			if msg.err != nil {
				b.liveErr = "OpenCode service unreachable: " + msg.err.Error()
			}
			b.store.SetReachable(false)
		}
		b.refreshRows()
		return b, nil
	case subscribedMsg:
		if msg.err != nil {
			b.liveErr = "OpenCode event stream: " + msg.err.Error()
			return b, b.scheduleReconnect()
		}
		b.backoff = 0
		cmds := []tea.Cmd{waitEvent(msg.ch)}
		for _, p := range b.projects {
			cmds = append(cmds, b.loadTasks(p))
		}
		return b, tea.Batch(cmds...)
	case eventMsg:
		if !msg.ok {
			b.liveErr = "OpenCode event stream disconnected; reconnecting"
			return b, b.scheduleReconnect()
		}
		var cmds []tea.Cmd
		if msg.ch != nil {
			cmds = append(cmds, waitEvent(msg.ch))
		}
		if msg.ev.SessionID != "" && opencode.Relevant(msg.ev.Type) {
			cmds = append(cmds, b.resolveRoot(msg.ev))
		}
		return b, tea.Batch(cmds...)
	case rootMsg:
		p, ok := b.store.ProjectForSession(msg.root)
		if !ok {
			return b, nil
		}
		var cmds []tea.Cmd
		if msg.settled {
			cmds = append(cmds, b.loadTasks(p))
		}
		if !b.pending[msg.root] {
			b.pending[msg.root] = true
			refresh := refreshMsg{id: msg.root}
			if c := b.after(300*time.Millisecond, refresh); c != nil {
				cmds = append(cmds, c)
			} else {
				cmds = append(cmds, func() tea.Msg { return refresh })
			}
		}
		return b, tea.Batch(cmds...)
	case refreshMsg:
		delete(b.pending, msg.id)
		return b, b.loadSnaps([]string{msg.id})
	case tickMsg:
		cmds := []tea.Cmd{b.after(b.reread, tickMsg{})}
		for _, p := range b.projects {
			cmds = append(cmds, b.loadTasks(p))
		}
		return b, tea.Batch(cmds...)
	case reconnectMsg:
		return b, b.subscribe()
	case jumpedMsg:
		if msg.err != nil {
			b.message = "jump failed: " + msg.err.Error()
			return b, nil
		}
		return b, tea.Quit
	}
	return b, nil
}

func (b *Board) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if b.editing {
		switch k {
		case "enter":
			b.editing = false
		case "esc":
			b.editing, b.filter = false, ""
		case "backspace":
			if r := []rune(b.filter); len(r) > 0 {
				b.filter = string(r[:len(r)-1])
			}
		default:
			b.filter += msg.Text
		}
		b.refreshRows()
		return b, nil
	}
	switch k {
	case "q", "esc", "ctrl+c":
		return b, tea.Quit
	case "j", "down":
		b.move(1)
	case "k", "up":
		b.move(-1)
	case "a":
		b.showAll = !b.showAll
		b.refreshRows()
	case "/":
		b.editing = true
	case "enter":
		return b, b.enter()
	}
	return b, nil
}

func (b *Board) enter() tea.Cmd {
	if len(b.rows) == 0 {
		return nil
	}
	row := b.rows[b.cursor]
	switch {
	case row.Display.State == status.Lost:
		b.message = row.Task.ID + ": its session is gone; dispatch it again with d in `tsk project`"
		return nil
	case row.Task.Meta(tracker.MetaSession) == "":
		b.message = row.Task.ID + " has not been dispatched; use `tsk project`"
		return nil
	case b.jump == nil:
		b.message = "herdr is not available, so tsk cannot jump to tasks"
		return nil
	}
	target := dispatch.TargetFor(row.Project, row.Task)
	jump := b.jump
	ctx := b.ctx
	return func() tea.Msg { return jumpedMsg{err: jump(ctx, target)} }
}

func (b *Board) refreshRows() {
	rows := b.store.Rows(b.showAll)
	if f := strings.ToLower(b.filter); f != "" {
		kept := rows[:0]
		for _, r := range rows {
			hay := strings.ToLower(r.Task.ID + " " + r.Task.Title + " " + r.Project.Name)
			if strings.Contains(hay, f) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	b.rows = rows
	found := false
	for i, r := range rows {
		if b.selected != (rowKey{}) && keyOf(r) == b.selected {
			b.cursor, found = i, true
		}
	}
	if !found {
		b.cursor = max(0, min(len(rows)-1, b.cursor))
		b.selected = rowKey{}
	}
}

func (b *Board) move(delta int) {
	b.cursor = max(0, min(len(b.rows)-1, b.cursor+delta))
	if len(b.rows) > 0 {
		b.selected = keyOf(b.rows[b.cursor])
	}
}

func (b *Board) View() tea.View {
	var s strings.Builder
	counts := map[status.State]int{}
	for _, r := range b.store.Rows(false) {
		counts[r.Display.State]++
	}
	fmt.Fprintf(&s, "tsk · %d waiting · %d in progress · %d review", counts[status.Waiting], counts[status.InProgress], counts[status.Review])
	if q := b.store.Queued(); len(q) > 0 {
		names := make([]string, 0, len(q))
		for n := range q {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s %d", n, q[n]))
		}
		fmt.Fprintf(&s, "    queued: %s", strings.Join(parts, ", "))
	}
	s.WriteString("\n")
	if b.liveErr != "" {
		fmt.Fprintf(&s, "! %s\n", b.liveErr)
	}
	for _, w := range b.warnings {
		fmt.Fprintf(&s, "! %s\n", w)
	}
	names := make([]string, 0, len(b.errs))
	for n := range b.errs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&s, "! %s: %s\n", n, b.errs[n])
	}
	if b.editing || b.filter != "" {
		fmt.Fprintf(&s, "filter: %s\n", b.filter)
	}
	s.WriteString("\n")
	if len(b.rows) == 0 {
		s.WriteString("  nothing to show\n")
	}
	group := status.State(-1)
	for i, r := range b.rows {
		if r.Display.State != group {
			group = r.Display.State
			fmt.Fprintf(&s, "%s\n", strings.ToUpper(group.String()))
		}
		cursor := "  "
		if i == b.cursor {
			cursor = "> "
		}
		fmt.Fprintf(&s, "%s%-10s %-4s %-40s %-11s %s\n", cursor, r.Task.ID, r.Project.Name,
			truncate(r.Task.Title, 40), r.Display.Detail, age(b.now().Sub(r.Since)))
	}
	if b.message != "" {
		fmt.Fprintf(&s, "\n%s\n", b.message)
	}
	s.WriteString("\nj/k move · enter jump · a all · / filter · q close\n")
	v := tea.NewView(s.String())
	v.AltScreen = true
	return v
}
