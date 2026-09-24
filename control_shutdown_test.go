package airplay2

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type pipeListener struct {
	connection chan net.Conn
	closed     chan struct{}
	once       sync.Once
}

func TestControlRecoversAfterStalledHeaders(t *testing.T) {
	s := newTestControlServer(t)
	s.cfg.Limits.MaxConnections = 1
	s.cfg.Limits.ReadHeaderTimeout = 15 * time.Millisecond
	l := &pipeListener{connection: make(chan net.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, l) }()
	for i := 0; i < 20; i++ {
		server, client := net.Pipe()
		l.connection <- server
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(client, "OPTIONS * RTSP/1.0\r\nCSeq:"); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err := client.Read(b[:]); err == nil {
			t.Fatal("partial request was accepted")
		}
		client.Close()
		server, client = net.Pipe()
		l.connection <- server
		client.SetDeadline(time.Now().Add(time.Second))
		if response := exchange(t, client, "OPTIONS * RTSP/1.0\r\nCSeq: 1\r\n\r\n"); statusCode(response.status) != "200" {
			t.Fatal(response.status)
		}
		client.Close()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control shutdown hung")
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connection:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestControlShutdownClosesActiveConnection(t *testing.T) {
	for _, cancelContext := range []bool{true, false} {
		s := newTestControlServer(t)
		server, client := net.Pipe()
		defer client.Close()
		l := &pipeListener{connection: make(chan net.Conn), closed: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.serve(ctx, l) }()
		l.connection <- server
		if cancelContext {
			cancel()
		} else {
			_ = l.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown waited on an active control connection")
		}
		if _, err := client.Write([]byte("OPTIONS * RTSP/1.0\r\n\r\n")); err == nil {
			t.Fatal("connection survived shutdown")
		}
	}
}
