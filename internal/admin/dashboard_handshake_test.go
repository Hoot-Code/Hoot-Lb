package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/admin/dashboard"
)

// TestDashboardWebSocketHandshake_ValidToken confirms a valid token
// completes the RFC 6455 upgrade and that the connection then
// receives periodic JSON snapshots matching the configured pools.
func TestDashboardWebSocketHandshake_ValidToken(t *testing.T) {
	baseURL, _, cfg, cleanup := startTestServer(t)
	defer cleanup()

	conn, _, err := dialDashboardWS(baseURL, testToken)
	if err != nil {
		t.Fatalf("dialDashboardWS: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	opcode, payload, err := readWSTextFrame(conn)
	if err != nil {
		t.Fatalf("readWSTextFrame: %v", err)
	}
	if opcode != 0x1 {
		t.Fatalf("expected text frame opcode 0x1, got %#x", opcode)
	}

	var snap dashboard.Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v\npayload: %s", err, payload)
	}

	if len(snap.Pools) != len(cfg.Pools) {
		t.Fatalf("expected %d pools in snapshot, got %d", len(cfg.Pools), len(snap.Pools))
	}
}

// TestDashboardWebSocketHandshake_InvalidToken confirms a wrong token
// is rejected with a non-101 response before the upgrade completes.
func TestDashboardWebSocketHandshake_InvalidToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	_, resp, err := dialDashboardWS(baseURL, "this-is-not-the-token")
	if err == nil {
		t.Fatal("expected handshake to be rejected, got success")
	}
	if resp == nil {
		t.Fatalf("expected an HTTP response describing the rejection, got none (err: %v)", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestDashboardWebSocketHandshake_MissingToken confirms a request
// with no token query parameter at all is also rejected before
// upgrade.
func TestDashboardWebSocketHandshake_MissingToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	_, resp, err := dialDashboardWS(baseURL, "")
	if err == nil {
		t.Fatal("expected handshake to be rejected, got success")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("expected 401, got resp=%v err=%v", resp, err)
	}
}
