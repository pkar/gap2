package airplay2

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// bindUnavailable reports whether the test environment forbids network binds,
// in which case listener-dependent tests skip rather than fail.
func bindUnavailable(t *testing.T) bool {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Logf("network bind unavailable: %v", err)
		return true
	}
	_ = ln.Close()
	return false
}

func waitAddr(t *testing.T, r *Receiver) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a := r.Addr(); a != nil {
			return a.String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("receiver never bound an address")
	return ""
}

func TestRunServeAndShutdown(t *testing.T) {
	if bindUnavailable(t) {
		t.Skip("network bind unavailable in sandbox")
	}
	r, err := New(Config{Name: "test", ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(ctx) }()

	addr := waitAddr(t, r)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /info HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read /info response: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "200 OK") {
		t.Fatalf("unexpected /info response: %q", buf[:n])
	}
	conn.Close()

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if got := r.Status().State; got != StateStopped {
		t.Fatalf("state after Run = %v, want stopped", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunTwice(t *testing.T) {
	if bindUnavailable(t) {
		t.Skip("network bind unavailable in sandbox")
	}
	r, _ := New(Config{ListenAddr: "127.0.0.1:0"})
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitAddr(t, r)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first Run = %v", err)
	}
	if err := r.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
}

func TestRunClosedBefore(t *testing.T) {
	r, _ := New(DefaultConfig())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Run after Close = %v, want ErrClosed", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	r, _ := New(DefaultConfig())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := r.Status().State; got != StateStopped {
		t.Fatalf("state = %v", got)
	}
}

func TestEventsClosedOnClose(t *testing.T) {
	r, _ := New(DefaultConfig())
	events := r.Events()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// The channel holds a buffered state event before it is closed; drain it
	// and verify the channel eventually closes.
	for {
		_, ok := <-events
		if !ok {
			return
		}
	}
}
