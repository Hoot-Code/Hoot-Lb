package admin

import (
	"net"
	"runtime"
	"testing"
	"time"
)

// TestDashboardWebSocketGoroutineLeak opens and cleanly closes many
// WebSocket connections in sequence and confirms the goroutine count
// returns to (approximately) its baseline — the same leak-detection
// pattern used by every other lifecycle test in this project.
func TestDashboardWebSocketGoroutineLeak(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const iterations = 30
	for i := 0; i < iterations; i++ {
		conn, _, err := dialDashboardWS(baseURL, testToken)
		if err != nil {
			t.Fatalf("iteration %d: dialDashboardWS: %v", i, err)
		}

		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := readWSTextFrame(conn); err != nil {
			t.Fatalf("iteration %d: readWSTextFrame: %v", i, err)
		}

		if err := sendWSClose(conn); err != nil {
			t.Fatalf("iteration %d: sendWSClose: %v", i, err)
		}
		conn.Close()
	}

	var after int
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		after = runtime.NumGoroutine()
		if after <= baseline+2 || time.Now().After(deadline) {
			break
		}
	}

	if after > baseline+2 {
		t.Fatalf("goroutine count did not return to baseline: baseline=%d after=%d (leak of ~%d goroutines across %d connections)",
			baseline, after, after-baseline, iterations)
	}
}

// TestDashboardWebSocketAbruptDisconnect opens a connection and then
// aborts the underlying TCP connection from the client side (RST,
// not a clean WebSocket close frame). It confirms the server's
// tracked-connection set drops the connection within a bounded time,
// proving the push goroutine isn't blocked forever on a dead write.
func TestDashboardWebSocketAbruptDisconnect(t *testing.T) {
	baseURL, srv, cleanup := startDashboardTestServer(t)
	defer cleanup()

	conn, _, err := dialDashboardWS(baseURL, testToken)
	if err != nil {
		t.Fatalf("dialDashboardWS: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := readWSTextFrame(conn); err != nil {
		t.Fatalf("readWSTextFrame: %v", err)
	}

	// Force an RST instead of a graceful FIN by setting a zero
	// linger timeout before closing — this is what "abrupt close"
	// looks like at the TCP level, as opposed to a clean WS Close
	// frame (covered by the goroutine-leak test above).
	if tcpConn, ok := conn.Conn.(*net.TCPConn); ok {
		tcpConn.SetLinger(0)
	}
	conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dashboardOpenConnCount(srv) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("server still tracked %d open dashboard connection(s) 5s after abrupt client disconnect",
		dashboardOpenConnCount(srv))
}
