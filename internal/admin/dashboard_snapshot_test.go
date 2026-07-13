package admin

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/admin/dashboard"
)

// TestDashboardSnapshotReflectsDrainWithinPushInterval drains a
// backend via the existing REST endpoint and confirms the *next*
// WebSocket push reports it as draining, within a small multiple of
// the configured push interval — no new backend logic is added here,
// only confirming the dashboard's read path observes the same
// AtomicSnapshot the REST handlers already mutate.
func TestDashboardSnapshotReflectsDrainWithinPushInterval(t *testing.T) {
	baseURL, _, cfg, cleanup := startTestServer(t)
	defer cleanup()

	pool := cfg.Pools[0].Name
	address := cfg.Pools[0].Backends[0].Address

	conn, _, err := dialDashboardWS(baseURL, testToken)
	if err != nil {
		t.Fatalf("dialDashboardWS: %v", err)
	}
	defer conn.Close()

	// Consume and discard the initial push (sent immediately on
	// connect) so we only look at pushes generated after the drain
	// call below.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := readWSTextFrame(conn); err != nil {
		t.Fatalf("reading initial snapshot: %v", err)
	}

	drainURL := fmt.Sprintf("%s/admin/pools/%s/backends/%s/drain", baseURL, pool, address)
	resp := doReq(t, "POST", drainURL, authHeader(), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("drain request: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	deadline := time.Now().Add(2*dashboard.PushInterval + 3*time.Second)
	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := readWSTextFrame(conn)
		if err != nil {
			t.Fatalf("readWSTextFrame: %v", err)
		}

		var snap dashboard.Snapshot
		if err := json.Unmarshal(payload, &snap); err != nil {
			t.Fatalf("unmarshal snapshot: %v", err)
		}

		if backendDraining(t, snap, pool, address) {
			return // observed the drained state — test passes.
		}

		if time.Now().After(deadline) {
			t.Fatalf("backend %s/%s not reported as draining within %s of the drain call",
				pool, address, 2*dashboard.PushInterval+3*time.Second)
		}
	}
}

func backendDraining(t *testing.T, snap dashboard.Snapshot, pool, address string) bool {
	t.Helper()
	for _, p := range snap.Pools {
		if p.Name != pool {
			continue
		}
		for _, b := range p.Backends {
			if b.Address == address {
				return b.Draining
			}
		}
	}
	return false
}
