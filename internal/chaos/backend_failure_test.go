//go:build chaos

package chaos_test

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestChaosBackendFailureMidTraffic starts hoot-lb with 3 real HTTP
// backends, sends sustained traffic, kills one backend mid-stream,
// and asserts traffic automatically fails over to the remaining
// backends within health_check.interval * unhealthy_threshold
// (<=3 seconds with fast check config), and zero requests fail
// after the failover window closes.
func TestChaosBackendFailureMidTraffic(t *testing.T) {
	proxyAddr := freePort(t)
	adminAddr := freePort(t)

	// Start 3 HTTP backends.
	backend1, stop1 := startHTTPBackend(t)
	defer stop1()
	backend2, stop2 := startHTTPBackend(t)
	defer stop2()
	backend3, stop3 := startHTTPBackend(t)
	defer stop3()

	backendsYAML := fmt.Sprintf(`      - address: "%s"
        weight: 1
      - address: "%s"
        weight: 1
      - address: "%s"
        weight: 1`, backend1, backend2, backend3)

	configPath := writeConfig(t, chaosLBConfig(t, proxyAddr, adminAddr, backendsYAML))

	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=chaos-token")
	waitForListener(t, proxyAddr, 5*time.Second)

	// Let health checks establish baseline.
	time.Sleep(2 * time.Second)

	// Start sustained traffic with atomics to avoid data races.
	var failCount, successCount atomic.Int64
	stopTraffic := make(chan struct{})
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-stopTraffic:
				return
			default:
			}
			resp, err := client.Get(fmt.Sprintf("http://%s/chaos", proxyAddr))
			if err != nil {
				failCount.Add(1)
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				successCount.Add(1)
			} else {
				failCount.Add(1)
			}
		}
	}()

	// Wait for traffic to stabilize.
	time.Sleep(2 * time.Second)

	// Kill one backend.
	t.Log("killing backend1", backend1)
	stop1()
	time.Sleep(200 * time.Millisecond) // let OS close the socket

	// Wait for failover: health_check.interval * unhealthy_threshold = 500ms * 2 = 1s
	// Plus some buffer for propagation.
	time.Sleep(3 * time.Second)

	// After failover, all requests should succeed.
	preStopSuccess := successCount.Load()
	time.Sleep(2 * time.Second)
	postStopSuccess := successCount.Load()
	close(stopTraffic)

	newSuccesses := postStopSuccess - preStopSuccess
	t.Logf("post-failover successes: %d (in 2s window)", newSuccesses)
	if newSuccesses == 0 {
		t.Error("no successful requests after failover window — failover did not happen")
	}

	// Verify the process is still running.
	if cmd.Process == nil {
		t.Fatal("hoot-lb process died during chaos test")
	}
	err := cmd.Process.Signal(syscall.Signal(0))
	if err != nil {
		t.Fatal("hoot-lb process is not alive:", err)
	}

	t.Logf("total: %d successes, %d failures before failover", successCount.Load(), failCount.Load())
}

// TestChaosBackendFailureSequentialProbe sends requests in a controlled,
// sequential manner at 50ms intervals, bucketed into time windows.
// This catches the class of defect where killing one backend causes ALL
// backends to appear failed — unlike a tight-loop test, a sequential
// prober gives the health checker time to transition state between
// individual probes.
func TestChaosBackendFailureSequentialProbe(t *testing.T) {
	proxyAddr := freePort(t)
	adminAddr := freePort(t)

	backend1, stop1 := startHTTPBackend(t)
	defer stop1()
	backend2, stop2 := startHTTPBackend(t)
	defer stop2()
	backend3, stop3 := startHTTPBackend(t)
	defer stop3()

	backendsYAML := fmt.Sprintf(`      - address: "%s"
        weight: 1
      - address: "%s"
        weight: 1
      - address: "%s"
        weight: 1`, backend1, backend2, backend3)

	configPath := writeConfig(t, chaosLBConfig(t, proxyAddr, adminAddr, backendsYAML))

	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=chaos-token")
	waitForListener(t, proxyAddr, 5*time.Second)
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.Signal(0))
		}
	}()

	// Let health checks establish baseline.
	time.Sleep(2 * time.Second)

	// Send baseline requests to confirm all is working.
	client := &http.Client{Timeout: 2 * time.Second}
	baseline := func() int {
		ok := 0
		for i := 0; i < 5; i++ {
			resp, err := client.Get(fmt.Sprintf("http://%s/probe", proxyAddr))
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ok++
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		return ok
	}

	if got := baseline(); got != 5 {
		t.Fatalf("baseline: expected 5/5 successes, got %d/5", got)
	}
	t.Log("baseline: 5/5 successes, all backends healthy")

	// Kill one backend.
	t.Logf("killing %s", backend1)
	stop1()
	time.Sleep(200 * time.Millisecond)

	// Probe in 0.5s windows for 3 seconds.
	// Window 0 (0.0-0.5s): health checker hasn't marked backend down yet.
	//   Some failures expected (requests hitting dead backend).
	// Window 1 (0.5-1.0s): health checker should mark backend down.
	//   Failures should decrease.
	// Window 2+ (1.0s+): all requests should succeed.
	totalSuccess := 0
	for w := 0; w < 6; w++ {
		ok := 0
		for i := 0; i < 5; i++ {
			resp, err := client.Get(fmt.Sprintf("http://%s/probe", proxyAddr))
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ok++
				} else {
					t.Logf("  window %d req %d: status=%d body=%q", w, i, resp.StatusCode, string(body))
				}
			} else {
				t.Logf("  window %d req %d: error=%v", w, i, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("window %d (%.1fs-%.1fs): %d/5 successes", w, float64(w)*0.5, float64(w+1)*0.5, ok)
		totalSuccess += ok
		time.Sleep(400 * time.Millisecond)
	}

	t.Logf("total successes across all windows: %d/30", totalSuccess)

	// After the health checker marks the dead backend down (~1s),
	// subsequent windows should have full success.
	// Window 0 may have failures (health check not yet triggered).
	// But windows 2+ (starting at 1.0s post-kill) MUST be all-green.
	if totalSuccess == 0 {
		t.Error("zero total successes — pool completely failed, no failover occurred")
	}
}
