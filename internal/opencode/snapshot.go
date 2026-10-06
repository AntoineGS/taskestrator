package opencode

import (
	"context"
	"errors"
	"fmt"

	"github.com/AntoineGS/taskestrator/internal/status"
)

const maxTree = 64

// Snapshot loads the live state of a task's session and its subagent
// sessions. active is the result of Active, shared across snapshots.
func (c *Client) Snapshot(ctx context.Context, id string, active map[string]bool) (status.Snapshot, error) {
	if _, err := c.GetSession(ctx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return status.Snapshot{}, nil
		}
		return status.Snapshot{}, err
	}
	snap := status.Snapshot{Exists: true}
	seen := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 && len(seen) < maxTree {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if active[cur] {
			snap.Running = true
		}
		forms, err := c.FormCount(ctx, cur)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return status.Snapshot{}, err
		}
		snap.Forms += forms
		perms, err := c.PermissionCount(ctx, cur)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return status.Snapshot{}, err
		}
		snap.Permissions += perms
		kids, err := c.Children(ctx, cur)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return status.Snapshot{}, err
		}
		for _, k := range kids {
			queue = append(queue, k.ID)
		}
	}
	return snap, nil
}

// Root follows parent links from id to its top-level session. Parent links
// never change, so they are cached for the client's lifetime.
func (c *Client) Root(ctx context.Context, id string) (string, error) {
	for range maxTree {
		c.mu.Lock()
		parent, ok := c.parents[id]
		c.mu.Unlock()
		if !ok {
			s, err := c.GetSession(ctx, id)
			if err != nil {
				return "", err
			}
			parent = s.ParentID
			c.mu.Lock()
			if c.parents == nil {
				c.parents = map[string]string{}
			}
			c.parents[id] = parent
			c.mu.Unlock()
		}
		if parent == "" {
			return id, nil
		}
		id = parent
	}
	return "", fmt.Errorf("session %s: parent chain longer than %d", id, maxTree)
}
