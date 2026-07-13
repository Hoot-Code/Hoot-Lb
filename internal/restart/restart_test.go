package restart

import (
	"net"
	"os"
	"testing"
)

func TestIsHandoffDefault(t *testing.T) {
	os.Unsetenv(envHandoff)
	if IsHandoff() {
		t.Fatal("expected IsHandoff to return false by default")
	}
}

func TestIsHandoffSet(t *testing.T) {
	os.Setenv(envHandoff, "1")
	defer os.Unsetenv(envHandoff)
	if !IsHandoff() {
		t.Fatal("expected IsHandoff to return true when env is set")
	}
}

func TestConfigPathEmpty(t *testing.T) {
	os.Unsetenv(envConfig)
	if got := ConfigPath(); got != "" {
		t.Fatalf("expected empty config path, got %q", got)
	}
}

func TestConfigPathSet(t *testing.T) {
	os.Setenv(envConfig, "/test/config.yaml")
	defer os.Unsetenv(envConfig)
	if got := ConfigPath(); got != "/test/config.yaml" {
		t.Fatalf("expected /test/config.yaml, got %q", got)
	}
}

func TestDoubleRestartGuard(t *testing.T) {
	if !inProgress.TryLock() {
		t.Fatal("expected TryLock to succeed initially")
	}
	if inProgress.TryLock() {
		t.Fatal("expected second TryLock to fail while held")
	}
	inProgress.Unlock()
	if !inProgress.TryLock() {
		t.Fatal("expected TryLock to succeed after unlock")
	}
	inProgress.Unlock()
}

func TestReconstructTCPListener(t *testing.T) {
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

	desc := ListenerDesc{Name: "test", Protocol: "tcp", File: f}
	reconstructed, err := ReconstructTCPListener(desc)
	if err != nil {
		t.Fatalf("ReconstructTCPListener: %v", err)
	}
	defer reconstructed.Close()

	if reconstructed.Addr().String() != ln.Addr().String() {
		t.Fatalf("addresses don't match: %s vs %s", reconstructed.Addr(), ln.Addr())
	}
}

func TestReconstructUDPConn(t *testing.T) {
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

	desc := ListenerDesc{Name: "test", Protocol: "udp", File: f}
	reconstructed, err := ReconstructUDPConn(desc)
	if err != nil {
		t.Fatalf("ReconstructUDPConn: %v", err)
	}
	defer reconstructed.Close()

	if reconstructed.LocalAddr().String() != conn.LocalAddr().String() {
		t.Fatalf("addresses don't match: %s vs %s", reconstructed.LocalAddr(), conn.LocalAddr())
	}
}

func TestSignalReadyNil(t *testing.T) {
	SignalReady(nil)
}

func TestUnsupported(t *testing.T) {
	_ = ErrUnsupported
	_ = ErrHandoffInProgress
}
