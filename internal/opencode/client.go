// Package opencode talks to the OpenCode v2 background service.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// ErrNotFound is returned when the service answers 404.
var ErrNotFound = errors.New("not found")

// Client is an OpenCode HTTP client. Use it as a pointer.
type Client struct {
	BaseURL  string
	Password string
	HTTP     *http.Client

	mu      sync.Mutex
	parents map[string]string
}

// Session is the part of an OpenCode session tsk uses.
type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Title    string `json:"title"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// CreateSession describes a session to create.
type CreateSession struct {
	Title     string
	Directory string
	Metadata  map[string]string
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.Password != "" {
		req.SetBasicAuth("opencode", c.Password)
	}
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func sessionPath(id string) string { return "/api/session/" + url.PathEscape(id) }

func (c *Client) CreateSession(ctx context.Context, in CreateSession) (Session, error) {
	body := map[string]any{"title": in.Title, "location": map[string]string{"directory": in.Directory}}
	if len(in.Metadata) > 0 {
		body["metadata"] = in.Metadata
	}
	var out struct {
		Data Session `json:"data"`
	}
	err := c.do(ctx, http.MethodPost, "/api/session", body, &out)
	return out.Data, err
}

func (c *Client) GetSession(ctx context.Context, id string) (Session, error) {
	var out struct {
		Data Session `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, sessionPath(id), nil, &out)
	return out.Data, err
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, sessionPath(id), nil, nil)
}

func (c *Client) Prompt(ctx context.Context, id, text string) error {
	return c.do(ctx, http.MethodPost, sessionPath(id)+"/prompt", map[string]string{"text": text}, nil)
}

func (c *Client) Children(ctx context.Context, id string) ([]Session, error) {
	var out struct {
		Data []Session `json:"data"`
	}
	q := url.Values{"parentID": {id}, "limit": {"100"}}
	err := c.do(ctx, http.MethodGet, "/api/session?"+q.Encode(), nil, &out)
	return out.Data, err
}

func (c *Client) Active(ctx context.Context) (map[string]bool, error) {
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session/active", nil, &out); err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(out.Data))
	for id := range out.Data {
		active[id] = true
	}
	return active, nil
}

func (c *Client) count(ctx context.Context, path string) (int, error) {
	var out struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return 0, err
	}
	return len(out.Data), nil
}

func (c *Client) FormCount(ctx context.Context, id string) (int, error) {
	return c.count(ctx, sessionPath(id)+"/form")
}

// FirstQuestion returns the text of the first pending question form of a
// session, or "" when there is none.
func (c *Client) FirstQuestion(ctx context.Context, id string) (string, error) {
	var out struct {
		Data []struct {
			Title    string `json:"title"`
			Metadata struct {
				Kind string `json:"kind"`
			} `json:"metadata"`
			Fields []struct {
				Title string `json:"title"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, sessionPath(id)+"/form", nil, &out); err != nil {
		return "", err
	}
	for _, f := range out.Data {
		if f.Metadata.Kind != "question" {
			continue
		}
		if len(f.Fields) > 0 {
			return f.Fields[0].Title, nil
		}
		return f.Title, nil
	}
	return "", nil
}

func (c *Client) PermissionCount(ctx context.Context, id string) (int, error) {
	return c.count(ctx, sessionPath(id)+"/permission")
}
