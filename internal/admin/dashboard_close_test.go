package admin

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestServerCloseTerminatesUpgradedWebSockets confirms Close doesn't
// just stop accepting new connections (what http.Server.Shutdown
// does on its own) — it also force-closes WebSocket connections that
// were already upgraded via Hijack before Close was called. Once a
// connection is hijacked, http.Server.Shutdown has no visibility into
// it at all, so without explicit tracking (Server.wsConns / Track /
// Untrack) those connections would simply keep running, pushing
// snapshots forever, past the admin server's own shutdown.
func TestServerCloseTerminatesUpgradedWebSockets(t *testing.T) {
	baseURL, srv, _ := startDashboardTestServer(t)

	conns := make([]*wsTestConn, 3)
	for i := range conns {
		conn, _, err := dialDashboardWS(baseURL, testToken)
		if err != nil {
			t.Fatalf("dialDashboardWS #%d: %v", i, err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := readWSTextFrame(conn); err != nil {
			t.Fatalf("reading initial snapshot #%d: %v", i, err)
		}
		conns[i] = conn
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	if n := dashboardOpenConnCount(srv); n != len(conns) {
		t.Fatalf("expected %d tracked connections before Close, got %d", len(conns), n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatalf("Server.Close: %v", err)
	}

	// Close's force-close loop calls conn.Close() on each tracked
	// connection, but the corresponding Untrack call runs later, in
	// the connection's own ServeHTTP goroutine once it notices the
	// close and returns (see Server.Close's doc comment) — that's
	// asynchronous with respect to Close itself, so poll with a
	// bound rather than asserting zero the instant Close returns.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n := dashboardOpenConnCount(srv); n == 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("expected 0 tracked connections shortly after Close, still have %d after 2s", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 16)
		if _, err := conn.Read(buf); err == nil {
			t.Fatalf("connection #%d: expected read to fail after Server.Close, got no error", i)
		} else if err != io.EOF {
			// Any error (EOF, "use of closed network connection",
			// connection reset) demonstrates the connection was
			// actually torn down; only an unexpected *success* would
			// indicate Close failed to terminate it.
			t.Logf("connection #%d closed as expected: %v", i, err)
		}
	}
}
