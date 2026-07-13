package admin

import (
	"io"
	"log/slog"
	"sync"
	"time"
)

// AuditLogger writes structured JSON audit log entries. It is safe
// for concurrent use.
type AuditLogger struct {
	mu     sync.Mutex
	w      io.Writer
	logger *slog.Logger
}

// NewAuditLogger creates an AuditLogger that writes JSON entries to w.
func NewAuditLogger(w io.Writer) *AuditLogger {
	return &AuditLogger{
		w:      w,
		logger: slog.New(slog.NewJSONHandler(w, nil)),
	}
}

// Log writes an audit log entry for a mutating admin API call.
func (al *AuditLogger) Log(action, role, tokenHint, outcome, detail string) {
	al.mu.Lock()
	defer al.mu.Unlock()

	al.logger.Info("admin_audit",
		slog.String("action", action),
		slog.String("role", role),
		slog.String("token_hint", tokenHint),
		slog.String("outcome", outcome),
		slog.String("detail", detail),
		slog.Time("timestamp", time.Now().UTC()),
	)
}

// LogMutation is a convenience method that logs a mutating admin API
// call with the matched role's token redacted.
func (al *AuditLogger) LogMutation(action, outcome, token string, role *role) {
	roleName := "unknown"
	tokenHint := "unknown"
	if role != nil {
		if role.permissions[PermConfig] {
			roleName = "admin"
		} else if role.permissions[PermBackends] {
			roleName = "operator"
		} else if role.permissions[PermDrain] {
			roleName = "operator"
		} else if role.permissions[PermRestart] {
			roleName = "operator"
		} else if role.permissions[PermRead] {
			roleName = "viewer"
		}
		tokenHint = tokenRedact(token)
	}

	al.Log(action, roleName, tokenHint, outcome, "")
}
