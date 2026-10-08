package btplugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maxLine = 4 << 20

// RPCError is a JSON-RPC error object. It is both sent and received.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Fail builds a plugin error (code -32000) carrying data.type.
func Fail(typ, msg string) *RPCError {
	data, _ := json.Marshal(map[string]string{"type": typ})
	return &RPCError{Code: -32000, Message: msg, Data: data}
}

// Handler serves requests and notifications from bt. The result of a
// notification is ignored.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type callResult struct {
	result json.RawMessage
	err    error
}

// Peer is one end of a JSON-RPC 2.0 connection.
type Peer struct {
	r       io.Reader
	w       io.Writer
	handler Handler

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan callResult
	closed  bool
}

func NewPeer(r io.Reader, w io.Writer, h func(ctx context.Context, method string, params json.RawMessage) (any, error)) *Peer {
	return &Peer{r: r, w: w, handler: h, pending: map[int64]chan callResult{}}
}

// Run reads messages until the input ends or ctx is done. Requests are
// handled in their own goroutine; notifications synchronously in arrival
// order. Pending Calls fail once Run returns.
func (p *Peer) Run(ctx context.Context) error {
	defer p.failPending(io.ErrClosedPipe)

	errc := make(chan error, 1)
	go func() { errc <- p.readLoop(ctx) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Peer) readLoop(ctx context.Context) error {
	sc := bufio.NewScanner(p.r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		hasID := len(m.ID) > 0 && string(m.ID) != "null"
		switch {
		case m.Method != "" && hasID:
			id := append(json.RawMessage(nil), m.ID...)
			params := append(json.RawMessage(nil), m.Params...)
			go p.serve(ctx, id, m.Method, params)
		case m.Method != "":
			if p.handler != nil {
				_, _ = p.handler(ctx, m.Method, append(json.RawMessage(nil), m.Params...))
			}
		case hasID:
			p.deliver(m)
		}
	}
	return sc.Err()
}

func (p *Peer) serve(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) {
	resp := response{JSONRPC: "2.0", ID: id}
	if p.handler == nil {
		resp.Error = &RPCError{Code: -32601, Message: "method not found: " + method}
	} else if res, err := p.handler(ctx, method, params); err != nil {
		var re *RPCError
		if errors.As(err, &re) {
			resp.Error = re
		} else {
			resp.Error = &RPCError{Code: -32603, Message: err.Error()}
		}
	} else if res == nil {
		resp.Result = json.RawMessage("null")
	} else {
		resp.Result = res
	}
	_ = p.write(resp)
}

func (p *Peer) deliver(m message) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	p.mu.Lock()
	ch, ok := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	if !ok {
		return
	}
	if m.Error != nil {
		ch <- callResult{err: m.Error}
		return
	}
	ch <- callResult{result: m.Result}
}

func (p *Peer) failPending(err error) {
	p.mu.Lock()
	p.closed = true
	pending := p.pending
	p.pending = map[int64]chan callResult{}
	p.mu.Unlock()
	for _, ch := range pending {
		ch <- callResult{err: err}
	}
}

func (p *Peer) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.w.Write(b)
	return err
}

// Notify sends a notification to bt.
func (p *Peer) Notify(method string, params any) error {
	return p.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", method, params})
}

// Call sends a request to bt and waits for its response, decoding the result
// into result when non-nil. An error response comes back as *RPCError.
func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	ch := make(chan callResult, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return io.ErrClosedPipe
	}
	p.nextID++
	id := p.nextID
	p.pending[id] = ch
	p.mu.Unlock()

	err := p.write(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params})
	if err != nil {
		p.forget(id)
		return err
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if result == nil || len(res.result) == 0 {
			return nil
		}
		return json.Unmarshal(res.result, result)
	case <-ctx.Done():
		p.forget(id)
		return ctx.Err()
	}
}

func (p *Peer) forget(id int64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}
