//go:build chaos

package chaos_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildBinary compiles the hoot-lb binary and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hoot-lb-test")
	cmd := exec.Command("go", "build", "-o", binary, "../../cmd/lb/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

// writeConfig writes a YAML config file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "chaos.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// startLB starts the hoot-lb binary with the given config and returns
// the exec.Cmd. The cleanup function kills the process.
func startLB(t *testing.T, configPath string, env ...string) *exec.Cmd {
	t.Helper()
	binary := buildBinary(t)
	cmd := exec.Command(binary, "-config", configPath)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	return cmd
}

// waitForListener polls until a TCP listener is ready at addr.
func waitForListener(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready within %s", addr, timeout)
}

// freePort returns a free TCP port on loopback.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// startHTTPBackend starts a simple HTTP server that returns 200.
func startHTTPBackend(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	go srv.Serve(ln)
	return ln.Addr().String(), func() { srv.Close() }
}

// chaosLBConfig generates a config with fast health checks for chaos testing.
func chaosLBConfig(t *testing.T, proxyAddr, adminAddr, backendAddrs string) string {
	t.Helper()
	return fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
  metrics:
    enabled: false
  admin:
    enabled: true
    address: "%s"
    token_env: HOOT_LB_ADMIN_TOKEN
  health_check:
    interval: 500ms
    timeout: 200ms
    unhealthy_threshold: 2
    healthy_threshold: 1
listeners:
  - name: http_listener
    address: "%s"
    protocol: http
    pool: backend_pool
pools:
  - name: backend_pool
    algorithm: round_robin
    health_check:
      type: http
      path: /
      interval: 500ms
      timeout: 200ms
      unhealthy_threshold: 2
      healthy_threshold: 1
    backends:
%s
`, adminAddr, proxyAddr, backendAddrs)
}
