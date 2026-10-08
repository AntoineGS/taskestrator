package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const testPassword = "s3cret-pw"

type world struct {
	sessions    map[string]Session
	forms       map[string]int
	permissions map[string]int
	active      []string
	gets        atomic.Int32
	lastBody    map[string]any
	mu          sync.Mutex
}

func (w *world) handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		user, pw, ok := r.BasicAuth()
		if !ok || user != "opencode" || pw != testPassword {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON := func(v any) { _ = json.NewEncoder(rw).Encode(v) }
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && path == "/api/session":
			w.mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&w.lastBody)
			w.mu.Unlock()
			writeJSON(map[string]any{"data": map[string]any{"id": "ses_new", "location": w.lastBody["location"]}})
		case r.Method == http.MethodGet && path == "/api/session/active":
			data := map[string]any{}
			for _, id := range w.active {
				data[id] = map[string]string{"type": "running"}
			}
			writeJSON(map[string]any{"data": data})
		case r.Method == http.MethodGet && path == "/api/session":
			kids := []Session{}
			for _, s := range w.sessions {
				if s.ParentID == r.URL.Query().Get("parentID") {
					kids = append(kids, s)
				}
			}
			writeJSON(map[string]any{"data": kids, "cursor": map[string]any{}})
		case strings.HasPrefix(path, "/api/session/"):
			rest := strings.Split(strings.TrimPrefix(path, "/api/session/"), "/")
			s, ok := w.sessions[rest[0]]
			if !ok {
				rw.WriteHeader(http.StatusNotFound)
				return
			}
			switch {
			case len(rest) == 1 && r.Method == http.MethodGet:
				w.gets.Add(1)
				writeJSON(map[string]any{"data": s})
			case len(rest) == 1 && r.Method == http.MethodDelete:
				rw.WriteHeader(http.StatusNoContent)
			case rest[1] == "form":
				forms := make([]map[string]any, w.forms[s.ID])
				for i := range forms {
					forms[i] = map[string]any{
						"id": "frm", "title": "Questions",
						"metadata": map[string]any{"kind": "question"},
						"fields":   []map[string]string{{"title": "Which DB?"}},
					}
				}
				writeJSON(map[string]any{"data": forms})
			case rest[1] == "permission":
				writeJSON(map[string]any{"data": make([]map[string]string, w.permissions[s.ID])})
			case rest[1] == "prompt":
				w.mu.Lock()
				_ = json.NewDecoder(r.Body).Decode(&w.lastBody)
				w.mu.Unlock()
				writeJSON(map[string]any{"data": map[string]string{}})
			default:
				rw.WriteHeader(http.StatusInternalServerError)
				_, _ = rw.Write([]byte("unexpected"))
			}
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	})
}

func newWorld(t *testing.T) (*world, *Client) {
	w := &world{sessions: map[string]Session{}, forms: map[string]int{}, permissions: map[string]int{}}
	srv := httptest.NewServer(w.handler())
	t.Cleanup(srv.Close)
	return w, &Client{BaseURL: srv.URL, Password: testPassword}
}

func session(id, parent string) Session { return Session{ID: id, ParentID: parent} }

func TestCreateSessionSendsLocationAndMetadata(t *testing.T) {
	w, c := newWorld(t)
	s, err := c.CreateSession(context.Background(), CreateSession{Title: "se-1: x", Directory: "/r/.worktrees/se-1", Metadata: map[string]string{"tsk.task": "se-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "ses_new" || s.Location.Directory != "/r/.worktrees/se-1" {
		t.Fatalf("session = %+v", s)
	}
	w.mu.Lock()
	if w.lastBody["title"] != "se-1: x" || w.lastBody["metadata"].(map[string]any)["tsk.task"] != "se-1" {
		t.Fatalf("body = %v", w.lastBody)
	}
	w.mu.Unlock()
}

func TestPromptSendsText(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_a"] = session("ses_a", "")
	if err := c.Prompt(context.Background(), "ses_a", "do it"); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	if w.lastBody["text"] != "do it" {
		t.Fatalf("body = %v", w.lastBody)
	}
	w.mu.Unlock()
}

func TestGetSessionNotFound(t *testing.T) {
	_, c := newWorld(t)
	if _, err := c.GetSession(context.Background(), "ses_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNeverContainPassword(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_a"] = session("ses_a", "")
	_, err := c.count(context.Background(), "/api/session/ses_a/other")
	if err == nil || strings.Contains(err.Error(), testPassword) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	c.Password = "wrong-pw"
	if _, err := c.Active(context.Background()); err == nil || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotCountsChildSessions(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_root"] = session("ses_root", "")
	w.sessions["ses_kid"] = session("ses_kid", "ses_root")
	w.sessions["ses_grandkid"] = session("ses_grandkid", "ses_kid")
	w.forms["ses_grandkid"] = 1
	w.permissions["ses_root"] = 1
	snap, err := c.Snapshot(context.Background(), "ses_root", map[string]bool{"ses_kid": true})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Exists || !snap.Running || snap.Forms != 1 || snap.Permissions != 1 {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestSnapshotMissingSession(t *testing.T) {
	_, c := newWorld(t)
	snap, err := c.Snapshot(context.Background(), "ses_gone", nil)
	if err != nil || snap.Exists {
		t.Fatalf("snap = %+v, err = %v", snap, err)
	}
}

func TestRootFollowsParentsAndCaches(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_root"] = session("ses_root", "")
	w.sessions["ses_kid"] = session("ses_kid", "ses_root")
	for range 2 {
		root, err := c.Root(context.Background(), "ses_kid")
		if err != nil || root != "ses_root" {
			t.Fatalf("root = %q, err = %v", root, err)
		}
	}
	if n := w.gets.Load(); n != 2 {
		t.Fatalf("GET session calls = %d, want 2 (the second lookup is cached)", n)
	}
}

func TestParseServiceURL(t *testing.T) {
	if got := parseServiceURL("http://127.0.0.1:49374/\n"); got != "http://127.0.0.1:49374" {
		t.Fatalf("got %q", got)
	}
	if got := parseServiceURL("not running\n"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPassword(t *testing.T) {
	dir := t.TempDir()
	if pw, err := readPassword(filepath.Join(dir, "none.json")); err != nil || pw != "" {
		t.Fatalf("missing: %q, %v", pw, err)
	}
	path := filepath.Join(dir, "service.json")
	if err := os.WriteFile(path, []byte(`{"password":"pw1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if pw, err := readPassword(path); err != nil || pw != "pw1" {
		t.Fatalf("got %q, %v", pw, err)
	}
}

func TestFirstQuestion(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_a"] = session("ses_a", "")
	w.sessions["ses_b"] = session("ses_b", "")
	w.forms["ses_a"] = 1
	for id, want := range map[string]string{"ses_a": "Which DB?", "ses_b": ""} {
		got, err := c.FirstQuestion(context.Background(), id)
		if err != nil || got != want {
			t.Fatalf("FirstQuestion(%s) = %q, %v; want %q", id, got, err, want)
		}
	}
}

func TestFirstQuestionSkipsNonQuestionAndFallsBackToTitle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/ses_a/form", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"data":[{"title":"Other","metadata":{"kind":"auth"},"fields":[{"title":"nope"}]},{"title":"Pick one","metadata":{"kind":"question"},"fields":[]}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL}
	got, err := c.FirstQuestion(context.Background(), "ses_a")
	if err != nil || got != "Pick one" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSnapshotCarriesQuestion(t *testing.T) {
	w, c := newWorld(t)
	w.sessions["ses_root"] = session("ses_root", "")
	w.sessions["ses_kid"] = session("ses_kid", "ses_root")
	w.forms["ses_kid"] = 1
	snap, err := c.Snapshot(context.Background(), "ses_root", nil)
	if err != nil || snap.Question != "Which DB?" || snap.Forms != 1 {
		t.Fatalf("snap = %+v, err = %v", snap, err)
	}
}
