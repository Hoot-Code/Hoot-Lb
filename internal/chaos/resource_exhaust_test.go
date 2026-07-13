//go:build chaos

package chaos_test

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

// TestChaosResourceExhaustionRecovery sets max_connections_per_listener
// to 50, opens 200 concurrent connections, confirms only 50 are
// served (the rest are refused with immediate close), then releases
// all 200, confirms new connections are accepted normally (no stuck
// state in the semaphore from the refusals).
func TestChaosResourceExhaustionRecovery(t *testing.T) {
	proxyAddr := freePort(t)
	adminAddr := freePort(t)

	backend, stopBackend := startHTTPBackend(t)
	defer stopBackend()

	backendsYAML := fmt.Sprintf(`      - address: "%s"
        weight: 1`, backend)

	// Config with max_connections_per_listener = 50.
	configContent := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
  metrics:
    enabled: false
  max_connections_per_listener: 50
  admin:
    enabled: true
    address: "%s"
    token_env: HOOT_LB_ADMIN_TOKEN
listeners:
  - name: http_listener
    address: "%s"
    protocol: http
    pool: backend_pool
pools:
  - name: backend_pool
    algorithm: round_robin
    backends:
      %s
`, adminAddr, proxyAddr, backendsYAML)
	configPath := writeConfig(t, configContent)

	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=chaos-token")
	waitForListener(t, proxyAddr, 5*time.Second)

	// Let it stabilize.
	time.Sleep(1 * time.Second)

	// Step 1: Open 200 concurrent connections.
	t.Log("step 1: opening 200 concurrent connections")
	const totalConns = 200
	conns := make([]net.Conn, 0, totalConns)
	var served, refused int

	for i := 0; i < totalConns; i++ {
		conn, err := net.DialTimeout("tcp", proxyAddr, 1*time.Second)
		if err != nil {
			refused++
			continue
		}
		// Try to make an HTTP request through the connection.
		conn.SetDeadline(time.Now().Add(1 * time.Second))
		_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
		if err != nil {
			conn.Close()
			refused++
			continue
		}
		// Read the response.
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			conn.Close()
			refused++
			continue
		}
		conns = append(conns, conn)
		served++
	}

	t.Logf("step 1: %d served, %d refused (total %d)", served, refused, totalConns)

	// Verify that roughly 50 connections were served (with some
	// tolerance for timing).
	if served < 40 || served > 60 {
		t.Errorf("expected ~50 served connections, got %d (refused: %d)", served, refused)
	}

	// Step 2: Release all connections.
	t.Log("step 2: releasing all connections")
	for _, conn := range conns {
		conn.Close()
	}
	time.Sleep(1 * time.Second)

	// Step 3: Verify new connections are accepted normally.
	t.Log("step 3: verifying recovery — new connections accepted")
	postConns := 0
	for i := 0; i < 20; i++ {
		resp, err := http.Get(fmt.Sprintf("http://%s/recovery", proxyAddr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				postConns++
			}
		}
	}

	t.Logf("step 3: %d/20 post-recovery requests succeeded", postConns)
	if postConns < 15 {
		t.Errorf("recovery failed: only %d/20 requests succeeded after resource exhaustion", postConns)
	}

	// Verify the process is still running.
	if cmd.Process == nil {
		t.Fatal("hoot-lb process died during resource exhaustion test")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("hoot-lb process is not alive:", err)
	}

	t.Log("resource exhaustion recovery verified: semaphore not stuck")
}
