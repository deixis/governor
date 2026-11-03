package mcp

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultDrainTimeout bounds how long a closed stdin waits for in-flight
// responses before EOF is passed on.
const DefaultDrainTimeout = 5 * time.Second

// NewDrainingTransport wraps a transport so that EOF from the peer is
// delivered only after every request read so far has been answered, or
// drain has elapsed.
//
// The SDK connection refuses all writes as soon as its reader returns EOF.
// A client that writes a request and closes its end of the pipe therefore
// never receives the response: the handler runs, the reply is dropped, and
// the server exits with "server is closing: EOF". Holding EOF back until the
// pending set is empty lets those replies through. Clients that keep the
// pipe open are unaffected; the wrapper only acts on EOF.
func NewDrainingTransport(inner sdkmcp.Transport, drain time.Duration) sdkmcp.Transport {
	if drain <= 0 {
		drain = DefaultDrainTimeout
	}
	return &drainingTransport{inner: inner, drain: drain}
}

type drainingTransport struct {
	inner sdkmcp.Transport
	drain time.Duration
}

func (t *drainingTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return newDrainingConn(c, t.drain), nil
}

// drainingConn tracks request IDs read but not yet answered.
type drainingConn struct {
	sdkmcp.Connection
	drain time.Duration

	mu      sync.Mutex
	pending map[jsonrpc.ID]struct{}
	idle    chan struct{} // closed when pending becomes empty; replaced on next request
}

func newDrainingConn(c sdkmcp.Connection, drain time.Duration) *drainingConn {
	idle := make(chan struct{})
	close(idle)
	return &drainingConn{Connection: c, drain: drain, pending: make(map[jsonrpc.ID]struct{}), idle: idle}
}

func (c *drainingConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err == nil {
		if req, ok := msg.(*jsonrpc.Request); ok && req.ID.IsValid() {
			c.track(req.ID)
		}
		return msg, nil
	}
	if !errors.Is(err, io.EOF) {
		return msg, err
	}

	// Peer closed its end. Let pending responses go out first.
	timer := time.NewTimer(c.drain)
	defer timer.Stop()
	select {
	case <-c.idleChan():
	case <-timer.C:
	case <-ctx.Done():
	}
	return msg, err
}

func (c *drainingConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	err := c.Connection.Write(ctx, msg)
	if resp, ok := msg.(*jsonrpc.Response); ok {
		c.settle(resp.ID)
	}
	return err
}

func (c *drainingConn) track(id jsonrpc.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		c.idle = make(chan struct{})
	}
	c.pending[id] = struct{}{}
}

func (c *drainingConn) settle(id jsonrpc.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pending[id]; !ok {
		return
	}
	delete(c.pending, id)
	if len(c.pending) == 0 {
		close(c.idle)
	}
}

func (c *drainingConn) idleChan() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idle
}
