package admin

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// startDashboardTestServer is like startTestServer (helpers_test.go)
// but also returns the *Server itself, so dashboard-specific tests
// can make white-box assertions against it (e.g. the size of its
// tracked-WebSocket-connection set) that the black-box HTTP-only
// helper doesn't expose.
func startDashboardTestServer(t *testing.T) (baseURL string, srv *Server, cleanup func()) {
	t.Helper()
	t.Setenv("HOOT_LB_TEST_ADMIN_TOKEN", testToken)

	cfg := &config.Config{
		Pools: []config.PoolConfig{
			{
				Name:      "static-pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "10.0.0.1:8080", Weight: 1},
					{Address: "10.0.0.2:8080", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{Type: "none"},
			},
		},
	}

	adminCfg := config.AdminConfig{
		Enabled:               boolPtr(true),
		Address:               ":0",
		TokenEnv:              "HOOT_LB_TEST_ADMIN_TOKEN",
		MaxConcurrentRequests: 10,
	}

	logger := testLogger()

	snap, err := runtime.BuildSnapshot(cfg, logger)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	atomicSnap := runtime.NewAtomicSnapshot(snap)

	adminSrv, err := NewServer(adminCfg, atomicSnap, cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go adminSrv.Start()
	time.Sleep(50 * time.Millisecond)

	base := fmt.Sprintf("http://%s", adminSrv.ln.Addr().String())

	cleanupFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		adminSrv.Close(ctx)
	}

	return base, adminSrv, cleanupFn
}

// dashboardOpenConnCount returns the number of dashboard WebSocket
// connections srv currently has tracked in wsConns.
func dashboardOpenConnCount(srv *Server) int {
	n := 0
	srv.wsConns.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// wsTestConn wraps a hijacked-style client connection to the
// dashboard's WebSocket endpoint. Reads must go through br (which may
// already hold buffered bytes left over from parsing the handshake
// response), not directly through the underlying net.Conn.
type wsTestConn struct {
	net.Conn
	br *bufio.Reader
}

// Read implements io.Reader by reading through br instead of the
// embedded net.Conn directly, so bytes buffered while parsing the
// HTTP handshake response aren't lost.
func (c *wsTestConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// dialDashboardWS performs a minimal RFC 6455 client-side handshake
// over a raw TCP connection against the admin server's dashboard
// WebSocket endpoint. There is no WebSocket client library available
// under this project's zero-dependency rule, so the handshake bytes
// are constructed and parsed directly, mirroring what a browser's
// WebSocket implementation does on the wire.
//
// On a successful 101 upgrade, it returns the open connection. On
// any other response (e.g. a 401 for a bad token) it returns the
// parsed *http.Response so callers can assert on the status code.
func dialDashboardWS(baseURL, token string) (*wsTestConn, *http.Response, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, nil, err
	}

	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := "/admin/ws"
	if token != "" {
		path += "?token=" + url.QueryEscape(token)
	} else {
		// Allow callers to explicitly test the "token entirely
		// absent" case rather than "token=empty-string".
		path += "?notoken=1"
	}

	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("dashboard ws handshake rejected: %s", resp.Status)
	}

	return &wsTestConn{Conn: conn, br: br}, resp, nil
}

// readWSTextFrame reads one server-to-client frame (server frames are
// unmasked, per RFC 6455 §5.1) and returns its opcode and payload.
func readWSTextFrame(c *wsTestConn) (opcode byte, payload []byte, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(c.br, head); err != nil {
		return 0, nil, err
	}
	opcode = head[0] & 0x0f
	length := uint64(head[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}

	payload = make([]byte, length)
	if length > 0 {
		if _, err = io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
	}
	return opcode, payload, nil
}

// sendWSClose writes a masked (client-to-server frames must be
// masked per RFC 6455 §5.1), zero-length Close frame.
func sendWSClose(c *wsTestConn) error {
	// FIN=1, opcode=0x8 (close); masked, length 0; zero mask key.
	_, err := c.Conn.Write([]byte{0x88, 0x80, 0x00, 0x00, 0x00, 0x00})
	return err
}
