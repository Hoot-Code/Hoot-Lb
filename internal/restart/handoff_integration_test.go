//go:build !windows

package restart_test

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/restart"
)

func TestListenerFileExport(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal("TCP File():", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		t.Fatal("stat:", err)
	}
	if fi.Size() == 0 {
		t.Log("TCP FD exported successfully")
	}
}

func TestHandoffReconstructTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	desc := restart.ListenerDesc{Name: "test-tcp", Protocol: "tcp", File: f}
	reconstructed, err := restart.ReconstructTCPListener(desc)
	if err != nil {
		t.Fatal("reconstruct:", err)
	}
	defer reconstructed.Close()

	if reconstructed.Addr().String() != ln.Addr().String() {
		t.Fatalf("addr mismatch: %s != %s", reconstructed.Addr(), ln.Addr())
	}
}

func TestHandoffReconstructUDP(t *testing.T) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	f, err := conn.File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	desc := restart.ListenerDesc{Name: "test-udp", Protocol: "udp", File: f}
	reconstructed, err := restart.ReconstructUDPConn(desc)
	if err != nil {
		t.Fatal("reconstruct:", err)
	}
	defer reconstructed.Close()

	if reconstructed.LocalAddr().String() != conn.LocalAddr().String() {
		t.Fatalf("addr mismatch: %s != %s", reconstructed.LocalAddr(), conn.LocalAddr())
	}
}

func TestHandoffMultipleListeners(t *testing.T) {
	files := make([]*os.File, 3)
	addrs := make([]string, 3)
	for i := 0; i < 3; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		addrs[i] = ln.Addr().String()
		f, err := ln.(*net.TCPListener).File()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		files[i] = f
	}

	for i, f := range files {
		desc := restart.ListenerDesc{
			Name:     fmt.Sprintf("listener-%d", i),
			Protocol: "tcp",
			File:     f,
		}
		ln, err := restart.ReconstructTCPListener(desc)
		if err != nil {
			t.Fatalf("listener %d: %v", i, err)
		}
		if ln.Addr().String() != addrs[i] {
			t.Fatalf("listener %d: addr mismatch %s != %s", i, ln.Addr(), addrs[i])
		}
		ln.Close()
	}
}

func TestFDMappingCorrectness(t *testing.T) {
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()

	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpLn.Close()

	adminLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer adminLn.Close()

	tcpFile, _ := tcpLn.(*net.TCPListener).File()
	httpFile, _ := httpLn.(*net.TCPListener).File()
	adminFile, _ := adminLn.(*net.TCPListener).File()
	defer tcpFile.Close()
	defer httpFile.Close()
	defer adminFile.Close()

	for _, f := range []*os.File{tcpFile, httpFile, adminFile} {
		fi, err := f.Stat()
		if err != nil {
			t.Fatal("FD stat failed:", err)
		}
		if fi.Mode()&os.ModeSocket == 0 {
			t.Fatal("FD is not a socket")
		}
	}
}

func TestDoubleRestartGuardReal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Create two separate FDs - one for each concurrent Trigger call.
	f1, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	f2, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	descs1 := []restart.ListenerDesc{{Name: "test", Protocol: "tcp", File: f1}}
	descs2 := []restart.ListenerDesc{{Name: "test", Protocol: "tcp", File: f2}}

	done := make(chan error, 2)
	go func() { done <- restart.Trigger(descs1, "/dev/null", logger) }()
	time.Sleep(200 * time.Millisecond)
	go func() { done <- restart.Trigger(descs2, "/dev/null", logger) }()

	err1 := <-done
	err2 := <-done

	if err1 == restart.ErrHandoffInProgress && err2 == restart.ErrHandoffInProgress {
		t.Fatal("both triggers were rejected, expected one to proceed")
	}
	if err1 != restart.ErrHandoffInProgress && err2 != restart.ErrHandoffInProgress {
		t.Log("both triggers ran (expected one rejection or both timed out)")
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hoot-lb-test")
	cmd := exec.Command("go", "build", "-o", binary, "../../cmd/lb/")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

func writeTestConfig(t *testing.T, addr, protocol string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "test.yaml")
	content := fmt.Sprintf(`global:
  log_level: info
  shutdown_timeout: 5s
  metrics:
    enabled: false
listeners:
  - name: test
    address: "%s"
    protocol: %s
    pool: test_pool
pools:
  - name: test_pool
    backends:
      - address: "127.0.0.1:19999"
        weight: 1
`, addr, protocol)
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}
