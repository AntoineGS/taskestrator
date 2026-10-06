package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Event is the part of an OpenCode event tsk uses.
type Event struct {
	Type      string
	SessionID string
}

// Subscribe streams events from the service. The channel closes when the
// connection ends or ctx is cancelled; callers reconnect.
func (c *Client) Subscribe(ctx context.Context) (<-chan Event, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/event", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /api/event: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET /api/event: HTTP %d", resp.StatusCode)
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		readEvents(resp.Body, func(e Event) bool {
			select {
			case ch <- e:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return ch, nil
}

func readEvents(r io.Reader, emit func(Event) bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var data strings.Builder
	flush := func() bool {
		defer data.Reset()
		if data.Len() == 0 {
			return true
		}
		if e, ok := parseEvent(data.String()); ok {
			return emit(e)
		}
		return true
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
}

func parseEvent(s string) (Event, bool) {
	var raw struct {
		Type string `json:"type"`
		Data struct {
			SessionID string `json:"sessionID"`
			Form      struct {
				SessionID string `json:"sessionID"`
			} `json:"form"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil || raw.Type == "" {
		return Event{}, false
	}
	id := raw.Data.SessionID
	if id == "" {
		id = raw.Data.Form.SessionID
	}
	return Event{Type: raw.Type, SessionID: id}, true
}

// Relevant reports whether an event can change a task's live state.
func Relevant(t string) bool {
	switch t {
	case "session.status", "session.idle", "session.deleted":
		return true
	}
	return strings.HasPrefix(t, "session.execution.") || strings.HasPrefix(t, "form.") || strings.HasPrefix(t, "permission.")
}

// Settled reports whether an event means the session stopped running, after
// which the agent may have updated its task in Beads.
func Settled(t string) bool {
	switch t {
	case "session.idle", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		return true
	}
	return false
}
