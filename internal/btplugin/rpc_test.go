package btplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type harness struct {
	toPlugin   io.WriteCloser
	fromPlugin *bufio.Scanner
	done       chan error
	cancel     context.CancelFunc
}

func newHarness(t *testing.T, h func(context.Context, string, json.RawMessage) (any, error)) (*harness, *Peer) {
	t.Helper()
	pr, pw := io.Pipe()
	br, bw := io.Pipe()
	p := NewPeer(pr, bw, h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	hs := &harness{toPlugin: pw, fromPlugin: sc, done: done, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		pw.Close()
		br.Close()
	})
	return hs, p
}

func (h *harness) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(h.toPlugin, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	if !h.fromPlugin.Scan() {
		t.Fatalf("no line from plugin: %v", h.fromPlugin.Err())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(h.fromPlugin.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInitializeRoundTrip(t *testing.T) {
	hs, _ := newHarness(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "initialize" {
			t.Errorf("method = %q", method)
		}
		return Manifest(), nil
	})
	hs.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"scope":{"mode":"global","databases":["tsk"]},"popup":false}}`)
	resp := hs.read(t)
	if string(resp["id"]) != "1" {
		t.Fatalf("id = %s", resp["id"])
	}
	var m struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Name            string `json:"name"`
		Subscribe       struct {
			Prefixes []string `json:"metadata_prefixes"`
			Statuses []string `json:"statuses"`
		} `json:"subscribe"`
		Fields []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Values []struct {
				ID    string `json:"id"`
				Badge string `json:"badge"`
				Tone  string `json:"tone"`
				Lane  *bool  `json:"lane"`
			} `json:"values"`
		} `json:"fields"`
		Sections []struct{ ID, Title string }      `json:"sections"`
		Actions  []struct{ ID, Label, Key string } `json:"actions"`
	}
	if err := json.Unmarshal(resp["result"], &m); err != nil {
		t.Fatal(err)
	}
	if m.ProtocolVersion != 1 || m.Name != "tsk" {
		t.Fatalf("manifest head = %+v", m)
	}
	if m.Actions[0].ID != "dispatch" || m.Actions[0].Key != "D" || m.Actions[1].ID != "jump" || m.Actions[1].Key != "J" {
		t.Fatalf("actions = %+v", m.Actions)
	}
	if len(m.Sections) != 1 || m.Sections[0].ID != "agent" || m.Sections[0].Title != "Agent" {
		t.Fatalf("sections = %+v", m.Sections)
	}
	if len(m.Subscribe.Prefixes) != 1 || m.Subscribe.Prefixes[0] != "tsk." || strings.Join(m.Subscribe.Statuses, ",") != "open,in_progress,review,blocked" {
		t.Fatalf("subscribe = %+v", m.Subscribe)
	}
	f := m.Fields[0]
	if f.ID != "state" || f.Label != "Agent" {
		t.Fatalf("field = %+v", f)
	}
	var ids []string
	for _, v := range f.Values {
		ids = append(ids, v.ID)
		switch v.ID {
		case "queued", "unknown", "untracked":
			if v.Lane == nil || *v.Lane {
				t.Errorf("%s lane = %v, want false", v.ID, v.Lane)
			}
		default:
			if v.Lane != nil {
				t.Errorf("%s lane should be omitted", v.ID)
			}
		}
	}
	if got := strings.Join(ids, ","); got != "waiting,running,review,lost,unknown,queued,untracked" {
		t.Fatalf("value order = %s", got)
	}
	if f.Values[0].Badge != "WAIT" || f.Values[0].Tone != "warn" || f.Values[5].Badge != "" {
		t.Fatalf("values = %+v", f.Values)
	}
}

func TestNotificationsInOrder(t *testing.T) {
	var got []string
	done := make(chan struct{})
	hs, _ := newHarness(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		got = append(got, string(params))
		if len(got) == 50 {
			close(done)
		}
		return nil, nil
	})
	for i := 0; i < 50; i++ {
		hs.send(t, `{"jsonrpc":"2.0","method":"beads.sync","params":`+itoa(i)+`}`)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	for i, g := range got {
		if g != itoa(i) {
			t.Fatalf("got[%d] = %s", i, g)
		}
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestRequestsRunConcurrently(t *testing.T) {
	release := make(chan struct{})
	hs, _ := newHarness(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "slow" {
			<-release
		}
		return method, nil
	})
	hs.send(t, `{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	hs.send(t, `{"jsonrpc":"2.0","id":2,"method":"fast"}`)
	if r := hs.read(t); string(r["id"]) != "2" {
		t.Fatalf("first response id = %s", r["id"])
	}
	close(release)
	if r := hs.read(t); string(r["id"]) != "1" {
		t.Fatalf("second response id = %s", r["id"])
	}
}

func TestErrors(t *testing.T) {
	hs, _ := newHarness(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "fail":
			return nil, Fail("no_action", "nope")
		case "plain":
			return nil, errors.New("boom")
		}
		return nil, &RPCError{Code: -32601, Message: "method not found"}
	})
	type errResp struct {
		Code    int
		Message string
		Data    struct{ Type string }
	}
	read := func() errResp {
		var e errResp
		if err := json.Unmarshal(hs.read(t)["error"], &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	hs.send(t, `{"jsonrpc":"2.0","id":"a","method":"fail"}`)
	if e := read(); e.Code != -32000 || e.Data.Type != "no_action" || e.Message != "nope" {
		t.Fatalf("fail = %+v", e)
	}
	hs.send(t, `{"jsonrpc":"2.0","id":3,"method":"plain"}`)
	if e := read(); e.Code != -32603 || e.Message != "boom" {
		t.Fatalf("plain = %+v", e)
	}
	hs.send(t, `{"jsonrpc":"2.0","id":4,"method":"other"}`)
	if e := read(); e.Code != -32601 {
		t.Fatalf("other = %+v", e)
	}
}

func TestNilHandlerUnknownMethod(t *testing.T) {
	hs, _ := newHarness(t, nil)
	hs.send(t, `{"jsonrpc":"2.0","id":9,"method":"x"}`)
	r := hs.read(t)
	if !strings.Contains(string(r["error"]), "-32601") {
		t.Fatalf("resp = %v", r)
	}
}

func TestStateSetEncoding(t *testing.T) {
	hs, p := newHarness(t, nil)
	since := time.Date(2026, 10, 7, 14, 3, 0, 0, time.UTC)
	errc := make(chan error, 1)
	go func() {
		errc <- p.Notify("state.set", StateSetParams{Beads: []BeadState{
			{DB: "tsk", ID: "tsk-a1b", Fields: map[string]FieldState{"state": {Value: "waiting", Since: &since}, "other": {Value: "x"}}, Actions: []string{"jump"}},
		}})
	}()
	line := hs.read(t)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if string(line["method"]) != `"state.set"` {
		t.Fatalf("line = %v", line)
	}
	if _, ok := line["id"]; ok {
		t.Fatal("notification has id")
	}
	s := string(line["params"])
	if strings.Contains(s, `"repo"`) {
		t.Errorf("repo present: %s", s)
	}
	if !strings.Contains(s, `"since":"2026-10-07T14:03:00Z"`) {
		t.Errorf("since missing: %s", s)
	}
	if strings.Count(s, `"since"`) != 1 {
		t.Errorf("since should be omitted when nil: %s", s)
	}
	if !strings.Contains(s, `"replace":false`) {
		t.Errorf("replace missing: %s", s)
	}
}

func TestBeadRepoAlwaysPresent(t *testing.T) {
	b, _ := json.Marshal(Bead{DB: "tsk", ID: "a"})
	if !strings.Contains(string(b), `"repo":null`) {
		t.Fatalf("bead = %s", b)
	}
	b, _ = json.Marshal(BeadRef{DB: "tsk", ID: "a"})
	if !strings.Contains(string(b), `"repo":null`) {
		t.Fatalf("ref = %s", b)
	}
}

func TestCall(t *testing.T) {
	hs, p := newHarness(t, nil)
	errc := make(chan error, 1)
	var res bool
	go func() { errc <- p.Call(context.Background(), "ui.confirm", map[string]string{"title": "t"}, &res) }()
	req := hs.read(t)
	if string(req["method"]) != `"ui.confirm"` {
		t.Fatalf("req = %v", req)
	}
	hs.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"result":true}`)
	if err := <-errc; err != nil || !res {
		t.Fatalf("err=%v res=%v", err, res)
	}

	go func() { errc <- p.Call(context.Background(), "ui.select", nil, nil) }()
	req = hs.read(t)
	hs.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"error":{"code":-32000,"message":"no","data":{"type":"no_action"}}}`)
	var re *RPCError
	if err := <-errc; !errors.As(err, &re) || re.Code != -32000 || !strings.Contains(string(re.Data), "no_action") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerConfirm(t *testing.T) {
	for result, want := range map[string]bool{"true": true, "false": false, "null": false} {
		t.Run(result, func(t *testing.T) {
			hs, p := newHarness(t, nil)
			s := &Server{Peer: p}
			type answer struct {
				ok  bool
				err error
			}
			done := make(chan answer, 1)
			go func() {
				ok, err := s.confirm(context.Background(), ConfirmParams{Title: "t", Message: "m"})
				done <- answer{ok, err}
			}()
			req := hs.read(t)
			if string(req["method"]) != `"ui.confirm"` || !strings.Contains(string(req["params"]), `"message":"m"`) {
				t.Fatalf("req = %v", req)
			}
			hs.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"result":`+result+`}`)
			if got := <-done; got.err != nil || got.ok != want {
				t.Fatalf("confirm = %+v, want %v", got, want)
			}
		})
	}
}

func TestCallContextCancel(t *testing.T) {
	hs, p := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- p.Call(ctx, "ui.select", nil, nil) }()
	hs.read(t)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPendingCallsFailWhenReaderEnds(t *testing.T) {
	hs, p := newHarness(t, nil)
	errc := make(chan error, 1)
	go func() { errc <- p.Call(context.Background(), "ui.confirm", nil, nil) }()
	hs.read(t)
	hs.toPlugin.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call still pending")
	}
	if err := <-hs.done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if err := p.Call(context.Background(), "ui.toast", nil, nil); err == nil {
		t.Fatal("call after close should fail")
	}
}

func TestLargeLine(t *testing.T) {
	got := make(chan int, 1)
	hs, _ := newHarness(t, func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		got <- len(params)
		return nil, nil
	})
	big := strings.Repeat("x", 3<<20)
	go io.WriteString(hs.toPlugin, `{"jsonrpc":"2.0","method":"beads.sync","params":"`+big+"\"}\n")
	select {
	case n := <-got:
		if n != len(big)+2 {
			t.Fatalf("len = %d", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	hs, _ := newHarness(t, nil)
	hs.cancel()
	select {
	case <-hs.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}
