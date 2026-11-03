package mcp

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// scriptedConn replays reads and records writes.
type scriptedConn struct {
	reads  []jsonrpc.Message // returned in order, then io.EOF
	writes chan jsonrpc.Message
}

func (s *scriptedConn) Read(context.Context) (jsonrpc.Message, error) {
	if len(s.reads) == 0 {
		return nil, io.EOF
	}
	m := s.reads[0]
	s.reads = s.reads[1:]
	return m, nil
}

func (s *scriptedConn) Write(_ context.Context, m jsonrpc.Message) error {
	s.writes <- m
	return nil
}
func (s *scriptedConn) Close() error      { return nil }
func (s *scriptedConn) SessionID() string { return "" }

func mustID(t *testing.T, v any) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(v)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDrainingConn_HoldsEOFUntilResponseWritten(t *testing.T) {
	id := mustID(t, float64(1))
	inner := &scriptedConn{
		reads:  []jsonrpc.Message{&jsonrpc.Request{ID: id, Method: "initialize"}},
		writes: make(chan jsonrpc.Message, 1),
	}
	c := newDrainingConn(inner, 2*time.Second)
	ctx := context.Background()

	if _, err := c.Read(ctx); err != nil {
		t.Fatalf("first read: %v", err)
	}

	eof := make(chan error, 1)
	go func() {
		_, err := c.Read(ctx)
		eof <- err
	}()

	select {
	case err := <-eof:
		t.Fatalf("EOF delivered before response was written: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := c.Write(ctx, &jsonrpc.Response{ID: id}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-eof:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF not delivered after response was written")
	}
}

func TestDrainingConn_TimesOut(t *testing.T) {
	id := mustID(t, "a")
	inner := &scriptedConn{
		reads:  []jsonrpc.Message{&jsonrpc.Request{ID: id, Method: "x"}},
		writes: make(chan jsonrpc.Message, 1),
	}
	c := newDrainingConn(inner, 50*time.Millisecond)
	ctx := context.Background()
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.Read(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if d := time.Since(start); d < 40*time.Millisecond || d > time.Second {
		t.Errorf("waited %v, want about 50ms", d)
	}
}

func TestDrainingConn_NotificationsAndIdleDoNotBlock(t *testing.T) {
	inner := &scriptedConn{
		reads:  []jsonrpc.Message{&jsonrpc.Request{Method: "notifications/initialized"}},
		writes: make(chan jsonrpc.Message, 1),
	}
	c := newDrainingConn(inner, 5*time.Second)
	ctx := context.Background()
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.Read(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("EOF was held although nothing was pending")
	}
}
