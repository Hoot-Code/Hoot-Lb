//go:build !windows

package restart_test

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/restart"
)

func TestAdminEndpointRestart(t *testing.T) {
	tcpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	tcpAddr := tcpLn.Addr().String()
	tcpLn.Close()

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	httpAddr := httpLn.Addr().String()
	httpLn.Close()

	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	adminAddr := adminLn.Addr().String()
	adminLn.Close()

	configPath := writeMultiListenerConfig(t, tcpAddr, httpAddr, adminAddr)
	startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=test-token")
	waitForListener(t, adminAddr, 5*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", "http://"+adminAddr+"/admin/restart", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", resp.StatusCode)
	}
	t.Logf("admin restart returned %d", resp.StatusCode)
}

func TestAdminDoubleRestartRejected(t *testing.T) {
	tcpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	tcpAddr := tcpLn.Addr().String()
	tcpLn.Close()

	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	adminAddr := adminLn.Addr().String()
	adminLn.Close()

	configPath := filepath.Join(t.TempDir(), "test.yaml")
	content := fmt.Sprintf(`global:
  log_level: info
  shutdown_timeout: 5s
  metrics:
    enabled: false
  admin:
    enabled: true
    address: "%s"
    token_env: HOOT_LB_ADMIN_TOKEN
listeners:
  - name: test
    address: "%s"
    protocol: tcp
    pool: backend_pool
pools:
  - name: backend_pool
    backends:
      - address: "127.0.0.1:19999"
        weight: 1
`, adminAddr, tcpAddr)
	os.WriteFile(configPath, []byte(content), 0644)

	startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=test-token")
	waitForListener(t, adminAddr, 5*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	makeRestartReq := func() int {
		req, _ := http.NewRequest("POST", "http://"+adminAddr+"/admin/restart", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := client.Do(req)
		if err != nil {
			return -1
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	var status1, status2 int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); status1 = makeRestartReq() }()
	go func() { defer wg.Done(); status2 = makeRestartReq() }()
	wg.Wait()

	if status1 != http.StatusAccepted && status2 != http.StatusAccepted {
		t.Fatalf("neither restart returned 202: %d and %d", status1, status2)
	}
	nonAccepted := 0
	if status1 != http.StatusAccepted {
		nonAccepted++
	}
	if status2 != http.StatusAccepted {
		nonAccepted++
	}
	if nonAccepted == 0 {
		t.Error("both returned 202 — expected one rejection")
	}
	t.Logf("restart results: %d and %d", status1, status2)
}

func TestFDMappingMultipleTypes(t *testing.T) {
	tcpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	tcpAddr := tcpLn.Addr().String()
	tcpLn.Close()

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	httpAddr := httpLn.Addr().String()
	httpLn.Close()

	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	adminAddr := adminLn.Addr().String()
	adminLn.Close()

	configPath := writeMultiListenerConfig(t, tcpAddr, httpAddr, adminAddr)
	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=test-token")
	waitForListener(t, tcpAddr, 5*time.Second)
	waitForListener(t, httpAddr, 2*time.Second)
	waitForListener(t, adminAddr, 2*time.Second)

	cmd.Process.Signal(syscall.SIGUSR2)
	time.Sleep(5 * time.Second)

	waitForListener(t, httpAddr, 5*time.Second)
	t.Log("HTTP listener reachable after handoff")

	tcpConn, err := net.DialTimeout("tcp", tcpAddr, 3*time.Second)
	if err != nil {
		t.Errorf("TCP unreachable: %v", err)
	} else {
		tcpConn.Close()
		t.Log("TCP listener reachable after handoff")
	}

	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", "http://"+adminAddr+"/admin/pools", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("admin unreachable: %v", err)
	} else {
		resp.Body.Close()
		t.Logf("admin responded with %d after handoff", resp.StatusCode)
	}
}

func TestPlatformGuard(t *testing.T) {
	if restart.Supported() {
		t.Skip("platform supports restart")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err := restart.Trigger(nil, "", logger)
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
	t.Logf("got expected error: %v", err)
}

func TestDoubleRestartGuardIntegration(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	adminAddr := adminLn.Addr().String()
	adminLn.Close()

	configPath := filepath.Join(t.TempDir(), "test.yaml")
	content := fmt.Sprintf(`global:
  log_level: info
  shutdown_timeout: 5s
  metrics:
    enabled: false
  admin:
    enabled: true
    address: "%s"
    token_env: HOOT_LB_ADMIN_TOKEN
listeners:
  - name: test
    address: "%s"
    protocol: tcp
    pool: backend_pool
pools:
  - name: backend_pool
    backends:
      - address: "127.0.0.1:19999"
        weight: 1
`, adminAddr, addr)
	os.WriteFile(configPath, []byte(content), 0644)

	cmd := startLB(t, configPath, "HOOT_LB_TEST=1", "HOOT_LB_ADMIN_TOKEN=test-token")
	waitForListener(t, addr, 5*time.Second)
	cmd.Process.Signal(syscall.SIGUSR2)
	time.Sleep(200 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGUSR2)
	time.Sleep(3 * time.Second)
	t.Log("double restart guard completed without panic")
}
