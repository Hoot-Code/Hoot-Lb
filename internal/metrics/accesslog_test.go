package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAccessLoggerOutput(t *testing.T) {
	var buf bytes.Buffer
	al := NewAccessLogger(&buf)
	al.Log(AccessLogEntry{
		Listener:   "web",
		Pool:       "backend",
		Backend:    "10.0.0.1:8080",
		Protocol:   "http",
		ClientAddr: "192.168.1.1",
		DurationMs: 42,
		Method:     "GET",
		Path:       "/api/v1",
		StatusCode: 200,
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var entry AccessLogEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Listener != "web" {
		t.Fatalf("expected listener 'web', got %q", entry.Listener)
	}
	if entry.Method != "GET" {
		t.Fatalf("expected method 'GET', got %q", entry.Method)
	}
	if entry.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", entry.StatusCode)
	}
}

func TestAccessLoggerL4OmitsHTTPFields(t *testing.T) {
	var buf bytes.Buffer
	al := NewAccessLogger(&buf)
	al.Log(AccessLogEntry{
		Listener:   "tcp_front",
		Pool:       "db_pool",
		Backend:    "10.0.0.2:5432",
		Protocol:   "tcp",
		ClientAddr: "10.0.0.3",
		DurationMs: 150,
		BytesSent:  1024,
	})

	var entry map[string]interface{}
	json.Unmarshal(buf.Bytes(), &entry)
	if _, ok := entry["method"]; ok {
		t.Fatal("method should be omitted for L4")
	}
	if _, ok := entry["status_code"]; ok {
		t.Fatal("status_code should be omitted for L4")
	}
}
