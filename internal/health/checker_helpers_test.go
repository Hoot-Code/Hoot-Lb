package health

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

func testLogger() *slog.Logger {
	return logging.New(slog.LevelError, os.Stdout)
}

func echoTCPServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					fmt.Fprintf(c, "%s\n", scanner.Text())
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}
