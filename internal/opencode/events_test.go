package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const stream = `data: {"id":"evt_1","type":"server.connected","data":{}}

: heartbeat

data: {"id":"evt_2","type":"session.execution.started","data":{"sessionID":"ses_a"}}

data: {"id":"evt_3","type":"form.created",
data: "data":{"form":{"id":"frm_1","sessionID":"ses_b"}}}

data: {not json}

data: {"id":"evt_4","type":"session.idle","data":{"sessionID":"ses_a"}}

`

func TestReadEvents(t *testing.T) {
	var got []Event
	readEvents(strings.NewReader(stream), func(e Event) bool { got = append(got, e); return true })
	want := []Event{
		{Type: "server.connected"},
		{Type: "session.execution.started", SessionID: "ses_a"},
		{Type: "form.created", SessionID: "ses_b"},
		{Type: "session.idle", SessionID: "ses_a"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadEventsStopsWhenEmitReturnsFalse(t *testing.T) {
	n := 0
	readEvents(strings.NewReader(stream), func(Event) bool { n++; return false })
	if n != 1 {
		t.Fatalf("emitted %d events", n)
	}
}

func TestSubscribeClosesWhenStreamEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if _, pw, _ := r.BasicAuth(); pw != testPassword || r.URL.Path != "/api/event" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte(stream))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Password: testPassword}
	ch, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if n != 4 {
					t.Fatalf("received %d events", n)
				}
				return
			}
			n++
		case <-timeout:
			t.Fatal("channel did not close")
		}
	}
}

func TestSubscribeRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Password: "wrong-pw"}
	_, err := c.Subscribe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("err = %v", err)
	}
}

func TestRelevantAndSettled(t *testing.T) {
	for _, typ := range []string{"session.status", "session.idle", "session.deleted", "session.execution.started", "form.created", "form.replied", "permission.asked", "permission.rejected"} {
		if !Relevant(typ) {
			t.Errorf("%s should be relevant", typ)
		}
	}
	for _, typ := range []string{"session.text.delta", "session.step.streamed", "server.connected"} {
		if Relevant(typ) {
			t.Errorf("%s should not be relevant", typ)
		}
	}
	if !Settled("session.execution.succeeded") || !Settled("session.idle") || Settled("session.execution.started") {
		t.Error("Settled returned the wrong values")
	}
}
