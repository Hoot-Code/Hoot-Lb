package metrics

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// AccessLogEntry represents a single completed connection or request.
type AccessLogEntry struct {
	Timestamp     string `json:"timestamp"`
	Listener      string `json:"listener"`
	Pool          string `json:"pool"`
	Backend       string `json:"backend"`
	Protocol      string `json:"protocol"`
	ClientAddr    string `json:"client_addr"`
	DurationMs    int64  `json:"duration_ms"`
	BytesSent     int64  `json:"bytes_sent"`
	BytesReceived int64  `json:"bytes_received"`
	// L7-only fields, omitted from JSON when empty.
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
}

// AccessLogger writes structured JSON access log entries to an
// injectable writer. It is safe for concurrent use.
type AccessLogger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewAccessLogger creates an AccessLogger that writes to w.
func NewAccessLogger(w io.Writer) *AccessLogger {
	return &AccessLogger{w: w}
}

// Log writes a single access log entry as a JSON line.
func (al *AccessLogger) Log(entry AccessLogEntry) {
	al.mu.Lock()
	defer al.mu.Unlock()

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	al.w.Write(data)
}
