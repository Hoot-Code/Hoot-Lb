//go:build !windows

package restart

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTriggerNoListeners(t *testing.T) {
	if err := Trigger(nil, "", nil); err == nil {
		t.Fatal("expected error for empty listeners")
	}
}

func TestHandoffRoundTrip(t *testing.T) {
	if os.Getenv("GO_TEST_CHILD") == "1" {
		runChild(t)
		return
	}

	// Start a TCP listener on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	// Get file descriptor.
	tcpLn := ln.(*net.TCPListener)
	f, err := tcpLn.File()
	if err != nil {
		t.Fatal(err)
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyW.Close()

	// Build the child command.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	extraFiles := []*os.File{f, readyW}
	readyFDNum := len(extraFiles) - 1 + 3

	cmd := exec.Command(exe, "-test.run=TestHandoffRoundTrip")
	cmd.ExtraFiles = extraFiles
	cmd.Env = append(os.Environ(),
		"GO_TEST_CHILD=1",
		envHandoff+"=1",
		envFDMap+"=test_listener:0",
		envConfig+"=test.yaml",
		envReadyFD+"="+fmt.Sprintf("%d", readyFDNum),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Wait for readiness.
	buf := make([]byte, 1)
	readyR.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := readyR.Read(buf)
	if err != nil {
		t.Fatalf("waiting for child readiness: %v", err)
	}
	if n != 1 || buf[0] != 'R' {
		t.Fatalf("unexpected readiness: n=%d byte=%q", n, buf[0])
	}

	// Verify the child can accept connections on the inherited FD.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("child not accepting: %v", err)
	}
	conn.Close()

	// Clean up child.
	cmd.Process.Kill()
	cmd.Wait()
}

func runChild(t *testing.T) {
	descs, readyW := ReconstructListeners()
	if len(descs) == 0 {
		t.Fatal("child: no inherited listeners")
	}

	ln, err := ReconstructTCPListener(descs[0])
	if err != nil {
		t.Fatalf("child: reconstruct: %v", err)
	}

	// Accept and immediately close.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	SignalReady(readyW)
	time.Sleep(2 * time.Second)
	os.Exit(0)
}

func TestReconstructListenersNoHandoff(t *testing.T) {
	os.Unsetenv(envHandoff)
	descs, readyW := ReconstructListeners()
	if descs != nil {
		t.Fatal("expected nil descriptors for non-handoff")
	}
	if readyW != nil {
		t.Fatal("expected nil readyW for non-handoff")
	}
}

func TestTriggerUnsupported(t *testing.T) {
	if Supported() {
		t.Skip("platform is supported, skipping unsupported test")
	}
	err := Trigger(nil, "", nil)
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
}

func TestFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	tcpLn := ln.(*net.TCPListener)
	f, err := tcpLn.File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_ = sockPath

	desc := ListenerDesc{Name: "test", File: f}
	reconstructed, err := ReconstructTCPListener(desc)
	if err != nil {
		t.Fatal(err)
	}
	defer reconstructed.Close()

	if reconstructed.Addr().String() != ln.Addr().String() {
		t.Fatalf("mismatch: %s != %s", reconstructed.Addr(), ln.Addr())
	}
}
