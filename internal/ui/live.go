// Package ui holds tsk's Bubble Tea views.
package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/status"
)

// Live is the part of the OpenCode client the views use.
type Live interface {
	Active(ctx context.Context) (map[string]bool, error)
	Snapshot(ctx context.Context, sessionID string, active map[string]bool) (status.Snapshot, error)
	Root(ctx context.Context, sessionID string) (string, error)
	Subscribe(ctx context.Context) (<-chan opencode.Event, error)
}

// Jumper focuses or reopens a task's session.
type Jumper func(ctx context.Context, t dispatch.Target) error

// After schedules msg after d. Tests replace it to avoid real timers.
type After func(d time.Duration, msg tea.Msg) tea.Cmd

func realAfter(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

const requestTimeout = 10 * time.Second

type snapsMsg struct {
	snaps     map[string]*status.Snapshot
	reachable bool
	err       error
}

type tickMsg struct{}

type jumpedMsg struct{ err error }

// loadSnapshots fetches the live state of each session. A session whose
// snapshot fails is recorded as nil, which shows as unknown.
func loadSnapshots(ctx context.Context, live Live, ids []string) snapsMsg {
	if live == nil {
		return snapsMsg{reachable: false}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	active, err := live.Active(ctx)
	if err != nil {
		return snapsMsg{reachable: false, err: err}
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
	return snapsMsg{snaps: snaps, reachable: true}
}
