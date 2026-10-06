package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/status"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Dispatcher runs a dispatch. It is nil when OpenCode or herdr is unavailable.
type Dispatcher func(ctx context.Context, req dispatch.Request) (dispatch.Result, error)

// ProjectConfig configures NewProject.
type ProjectConfig struct {
	Project  discover.Project
	File     config.ProjectFile
	Tracker  tracker.Tracker
	Live     Live
	Jump     Jumper
	Dispatch Dispatcher
	Git      gitwt.Git
	Reread   time.Duration
	After    After
	Warnings []string
}

type projectRow struct {
	task    tracker.Task
	display status.Display
	ready   bool
}

// Project is one project's task list with dispatch.
type Project struct {
	ctx context.Context
	cfg ProjectConfig

	tasks     []tracker.Task
	ready     map[string]int
	snaps     map[string]*status.Snapshot
	reachable bool
	snapsSeen bool
	snapErr   error
	rows      []projectRow
	cursor    int
	selected  string

	checked   bool
	reviewOK  bool
	ignoredOK bool
	busy      bool
	sent      map[string]string
	loadErr   string
	message   string
}

type projectLoadMsg struct {
	tasks []tracker.Task
	ready []tracker.Task
	err   error
}

type checksMsg struct {
	reviewOK, ignoredOK bool
	err                 error
}

type dispatchedMsg struct {
	id  string
	was string
	res dispatch.Result
	err error
}

type reviewAddedMsg struct{ err error }

// NewProject returns the project model.
func NewProject(ctx context.Context, cfg ProjectConfig) *Project {
	if cfg.After == nil {
		cfg.After = realAfter
	}
	if cfg.Reread <= 0 {
		cfg.Reread = 10 * time.Second
	}
	return &Project{ctx: ctx, cfg: cfg, ready: map[string]int{}, sent: map[string]string{}, snaps: map[string]*status.Snapshot{}}
}

func (p *Project) Init() tea.Cmd {
	return tea.Batch(p.load(), p.checks(), p.cfg.After(p.cfg.Reread, tickMsg{}))
}

func (p *Project) load() tea.Cmd {
	ctx, tr, repo := p.ctx, p.cfg.Tracker, p.cfg.Project.Repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		tasks, err := tr.List(ctx, repo)
		if err != nil {
			return projectLoadMsg{err: err}
		}
		ready, err := tr.Ready(ctx, repo)
		return projectLoadMsg{tasks: tasks, ready: ready, err: err}
	}
}

func (p *Project) checks() tea.Cmd {
	ctx, tr, git, repo := p.ctx, p.cfg.Tracker, p.cfg.Git, p.cfg.Project.Repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		reviewOK, err := tr.HasReviewStatus(ctx, repo)
		if err != nil {
			return checksMsg{err: err}
		}
		ignoredOK, err := git.IsIgnored(ctx, repo, ".worktrees/tsk-check")
		return checksMsg{reviewOK: reviewOK, ignoredOK: ignoredOK, err: err}
	}
}

func (p *Project) sessions() []string {
	var ids []string
	for _, t := range p.tasks {
		if id := t.Meta(tracker.MetaSession); id != "" && t.Status != "closed" && t.Status != tracker.StatusReview {
			ids = append(ids, id)
		}
	}
	return ids
}

func (p *Project) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.key(msg)
	case projectLoadMsg:
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			return p, nil
		}
		p.loadErr = ""
		p.tasks = msg.tasks
		p.pruneSent()
		p.ready = map[string]int{}
		for i, t := range msg.ready {
			p.ready[t.ID] = i
		}
		p.rebuild()
		ctx, live, ids := p.ctx, p.cfg.Live, p.sessions()
		return p, func() tea.Msg { return loadSnapshots(ctx, live, ids) }
	case snapsMsg:
		p.reachable, p.snapsSeen, p.snapErr = msg.reachable, true, msg.err
		if msg.reachable {
			for id, s := range msg.snaps {
				p.snaps[id] = s
			}
		}
		p.rebuild()
		return p, nil
	case checksMsg:
		p.checked = true
		if msg.err != nil {
			p.message = "setup check failed: " + msg.err.Error()
		}
		p.reviewOK, p.ignoredOK = msg.reviewOK, msg.ignoredOK
		return p, nil
	case reviewAddedMsg:
		if msg.err != nil {
			p.message = "could not add the review status: " + msg.err.Error()
			return p, nil
		}
		p.message = "added the review status"
		return p, p.checks()
	case dispatchedMsg:
		p.busy = false
		if msg.err == nil {
			p.sent[msg.id] = msg.was
		}
		switch {
		case msg.err != nil:
			p.message = "dispatch failed: " + msg.err.Error()
		case msg.res.TabError != nil:
			p.message = fmt.Sprintf("dispatched %s; the agent is working but tsk could not open its tab: %v", msg.id, msg.res.TabError)
		default:
			p.message = "dispatched " + msg.id
		}
		return p, p.load()
	case tickMsg:
		return p, tea.Batch(p.load(), p.cfg.After(p.cfg.Reread, tickMsg{}))
	case jumpedMsg:
		if msg.err != nil {
			p.message = "jump failed: " + msg.err.Error()
			return p, nil
		}
		return p, tea.Quit
	}
	return p, nil
}

// A dispatched task stays blocked until a load shows a new session or the task gone/closed, so stale loads cannot re-enable it.
func (p *Project) pruneSent() {
	for id, was := range p.sent {
		keep := false
		for _, t := range p.tasks {
			if t.ID == id {
				keep = t.Status != "closed" && t.Meta(tracker.MetaSession) == was
			}
		}
		if !keep {
			delete(p.sent, id)
		}
	}
}

func (p *Project) rebuild() {
	var rows []projectRow
	for _, t := range p.tasks {
		if t.Status == "closed" {
			continue
		}
		sid := t.Meta(tracker.MetaSession)
		var snap *status.Snapshot
		if p.reachable {
			snap = p.snaps[sid]
		}
		_, ready := p.ready[t.ID]
		rows = append(rows, projectRow{task: t, display: status.Derive(t.Status, sid, snap), ready: ready})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.ready != b.ready {
			return a.ready
		}
		if a.ready {
			return p.ready[a.task.ID] < p.ready[b.task.ID]
		}
		if a.display.State != b.display.State {
			return a.display.State < b.display.State
		}
		return a.task.ID < b.task.ID
	})
	p.rows = rows
	found := false
	for i, r := range rows {
		if p.selected != "" && r.task.ID == p.selected {
			p.cursor, found = i, true
		}
	}
	if !found {
		p.cursor = max(0, min(len(rows)-1, p.cursor))
		p.selected = ""
	}
}

func (p *Project) move(delta int) {
	p.cursor = max(0, min(len(p.rows)-1, p.cursor+delta))
	if len(p.rows) > 0 {
		p.selected = p.rows[p.cursor].task.ID
	}
}

func (p *Project) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		if p.busy {
			p.message = "dispatch in progress; wait for it to finish"
			return p, nil
		}
		return p, tea.Quit
	case "j", "down":
		p.move(1)
	case "k", "up":
		p.move(-1)
	case "R":
		ctx, tr, repo := p.ctx, p.cfg.Tracker, p.cfg.Project.Repo
		return p, func() tea.Msg { return reviewAddedMsg{err: tr.AddReviewStatus(ctx, repo)} }
	case "d":
		return p, p.dispatch()
	case "enter":
		return p, p.enter()
	}
	return p, nil
}

func (p *Project) dispatch() tea.Cmd {
	if p.busy || len(p.rows) == 0 {
		return nil
	}
	row := p.rows[p.cursor]
	dispatchable := (row.ready && row.display.State == status.Queued) || row.display.State == status.Lost
	_, sent := p.sent[row.task.ID]
	switch {
	case sent:
		p.message = row.task.ID + " was already dispatched; waiting for it to update"
		return nil
	case !dispatchable:
		p.message = row.task.ID + " is not ready to dispatch (" + row.display.String() + ")"
		return nil
	case !p.reviewOK:
		p.message = "add the review status first: press R"
		return nil
	case !p.ignoredOK:
		p.message = ".worktrees/ is not git-ignored in this repo; add it to .gitignore first"
		return nil
	case p.cfg.Dispatch == nil:
		p.message = "OpenCode or herdr is not available, so tsk cannot dispatch"
		return nil
	}
	p.busy = true
	p.message = "dispatching " + row.task.ID + "…"
	ctx, run := p.ctx, p.cfg.Dispatch
	req := dispatch.Request{Project: p.cfg.Project, Task: row.task, File: p.cfg.File}
	was := row.task.Meta(tracker.MetaSession)
	return func() tea.Msg {
		res, err := run(ctx, req)
		return dispatchedMsg{id: req.Task.ID, was: was, res: res, err: err}
	}
}

func (p *Project) enter() tea.Cmd {
	if len(p.rows) == 0 {
		return nil
	}
	row := p.rows[p.cursor]
	switch {
	case row.task.Meta(tracker.MetaSession) == "":
		p.message = row.task.ID + " has not been dispatched; press d"
		return nil
	case row.display.State == status.Lost:
		p.message = row.task.ID + ": its session is gone; press d to dispatch it again"
		return nil
	case p.cfg.Jump == nil:
		p.message = "herdr is not available, so tsk cannot jump to tasks"
		return nil
	}
	ctx, jump := p.ctx, p.cfg.Jump
	target := dispatch.TargetFor(p.cfg.Project, row.task)
	return func() tea.Msg { return jumpedMsg{err: jump(ctx, target)} }
}

func (p *Project) View() tea.View {
	var s strings.Builder
	fmt.Fprintf(&s, "%s  (%s)\n", p.cfg.Project.Repo, p.cfg.Project.Name)
	for _, w := range p.cfg.Warnings {
		fmt.Fprintf(&s, "! %s\n", w)
	}
	if p.loadErr != "" {
		fmt.Fprintf(&s, "! %s\n", p.loadErr)
	}
	if p.cfg.Live != nil && p.snapsSeen && !p.reachable {
		s.WriteString("! OpenCode service unreachable; live states are unknown")
		if p.snapErr != nil {
			fmt.Fprintf(&s, " (%v)", p.snapErr)
		}
		s.WriteString("\n")
	}
	if p.checked && !p.reviewOK {
		s.WriteString("! this project has no review status; press R to add it\n")
	}
	if p.checked && !p.ignoredOK {
		s.WriteString("! .worktrees/ is not git-ignored; add it to .gitignore\n")
	}
	s.WriteString("\n")
	if len(p.rows) == 0 {
		s.WriteString("  no open tasks\n")
	}
	header := ""
	for i, r := range p.rows {
		h := "OTHER"
		if r.ready {
			h = "READY"
		}
		if h != header {
			header = h
			fmt.Fprintf(&s, "%s\n", h)
		}
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		fmt.Fprintf(&s, "%s%-10s P%d  %-40s %s\n", cursor, r.task.ID, r.task.Priority, truncate(r.task.Title, 40), r.display)
	}
	if p.message != "" {
		fmt.Fprintf(&s, "\n%s\n", p.message)
	}
	s.WriteString("\nj/k move · enter jump · d dispatch · R add review status · q quit\n")
	v := tea.NewView(s.String())
	v.AltScreen = true
	return v
}
