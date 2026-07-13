//go:build chaos

package chaos_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestChaosSIGUSR2RestartUnderLoad runs with 10 concurrent client
// goroutines for 10 seconds, asserting strictly 0 dropped connections
// under real concurrency pressure during a zero-downtime binary
// restart — testing the FD handoff path specifically.
func TestChaosSIGUSR2RestartUnderLoad(t *testing.T) {
	// Build binary and set up multi-listener config.
	binary := buildBinary(t)
	tcpAddr := freePort(t)
	httpAddr := freePort(t)
	adminAddr := freePort(t)

	configPath := filepath.Join(t.TempDir(), "restart.yaml")
	content := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
  metrics:
    enabled: false
  admin:
    enabled: true
    address: "%s"
    token_env: HOOT_LB_ADMIN_TOKEN
listeners:
  - name: tcp_listener
    address: "%s"
    protocol: tcp
    pool: backend_pool
  - name: http_listener
    address: "%s"
    protocol: http
    pool: backend_pool
pools:
  - name: backend_pool
    backends:
      - address: "127.0.0.1:19999"
        weight: 1
`, adminAddr, tcpAddr, httpAddr)
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "-config", configPath)
	cmd.Env = append(os.Environ(), "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=restart-token")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
		}
	}()

	waitForListener(t, tcpAddr, 5*time.Second)

	// Start 10 concurrent client goroutines sending TCP connections.
	var failCount, attemptCount atomic.Int64
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				attemptCount.Add(1)
				conn, err := net.DialTimeout("tcp", tcpAddr, 2*time.Second)
				if err != nil {
					failCount.Add(1)
					continue
				}
				conn.Close()
			}
		}()
	}

	// Let traffic stabilize.
	time.Sleep(1 * time.Second)

	// Trigger SIGUSR2 restart.
	t.Log("sending SIGUSR2 for zero-downtime restart")
	cmd.Process.Signal(syscall.SIGUSR2)

	// Continue traffic for 10 seconds during and after restart.
	time.Sleep(10 * time.Second)
	close(stopCh)
	wg.Wait()

	attempts := attemptCount.Load()
	fails := failCount.Load()
	t.Logf("traffic: %d attempts, %d failures (%.1f%%)", attempts, fails, float64(fails)/float64(attempts)*100)

	// Assert zero dropped connections.
	if fails > 0 {
		t.Errorf("zero-downtime violated under load: %d/%d connections failed (%.1f%%)",
			fails, attempts, float64(fails)/float64(attempts)*100)
	}

	// Verify child is serving.
	childServing := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", tcpAddr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			childServing = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !childServing {
		t.Fatal("child not serving after SIGUSR2 restart")
	}
	t.Log("child serving after SIGUSR2 restart under load")
}
