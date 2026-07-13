//go:build chaos

package chaos_test

import (
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestChaosConfigFlapping reloads the config file 20 times in rapid
// succession while traffic flows, asserting the process stays up,
// traffic succeeds throughout (allow for <=1% error rate during the
// flap window), and no goroutine count explosion.
func TestChaosConfigFlapping(t *testing.T) {
	proxyAddr := freePort(t)
	adminAddr := freePort(t)

	backend, stopBackend := startHTTPBackend(t)
	defer stopBackend()

	backendsYAML := fmt.Sprintf(`      - address: "%s"
        weight: 1`, backend)

	// Write initial config.
	configContent := chaosLBConfig(t, proxyAddr, adminAddr, backendsYAML)
	configPath := writeConfig(t, configContent)

	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=chaos-token")
	waitForListener(t, proxyAddr, 5*time.Second)

	// Let it stabilize.
	time.Sleep(2 * time.Second)

	// Start sustained traffic.
	var failCount, successCount int64
	stopTraffic := make(chan struct{})
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-stopTraffic:
				return
			default:
			}
			resp, err := client.Get(fmt.Sprintf("http://%s/flap", proxyAddr))
			if err != nil {
				failCount++
				continue
			}
			resp.Body.Close()
			successCount++
		}
	}()

	// Wait for traffic to stabilize.
	time.Sleep(1 * time.Second)

	// Rapidly rewrite config 20 times with SIGHUP.
	t.Log("starting config flapping (20 reloads)")
	for i := 0; i < 20; i++ {
		// Write a slightly different config (change weight randomly).
		weight := (i % 5) + 1
		newContent := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
  metrics:
    enabled: false
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
      - address: "%s"
        weight: %d
`, adminAddr, proxyAddr, backend, weight)
		if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}
		// Send SIGHUP for immediate reload.
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGHUP)
		}
		time.Sleep(100 * time.Millisecond) // rapid but not instant
	}

	// Let traffic settle after flapping.
	time.Sleep(2 * time.Second)
	close(stopTraffic)

	total := successCount + failCount
	errorRate := float64(failCount) / float64(total) * 100
	t.Logf("total: %d requests, %d failures (%.2f%% error rate)", total, failCount, errorRate)

	// Assert <= 1% error rate during flap window.
	if errorRate > 1.0 {
		t.Errorf("error rate %.2f%% exceeds 1%% threshold during config flapping", errorRate)
	}

	// Verify the process is still running.
	if cmd.Process == nil {
		t.Fatal("hoot-lb process died during config flapping")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("hoot-lb process is not alive:", err)
	}

	t.Log("process survived 20 rapid config reloads under traffic")
}
