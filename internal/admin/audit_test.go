package admin

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuditLogNeverContainsFullToken(t *testing.T) {
	fullToken := "super-secret-admin-token-12345"
	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	al.LogMutation("drain", "success", fullToken, &role{
		token:       fullToken,
		permissions: map[string]bool{PermDrain: true},
	})

	output := buf.String()
	if strings.Contains(output, fullToken) {
		t.Errorf("audit log contains full token! Output: %s", output)
	}

	// Should contain redacted form (last 4 chars).
	if !strings.Contains(output, "...") {
		t.Errorf("audit log should contain redacted token hint, got: %s", output)
	}

	// Should NOT contain the full token.
	if strings.Contains(output, "super-secret-admin-token") {
		t.Errorf("audit log should not contain full token")
	}

	// Should contain the action.
	if !strings.Contains(output, "drain") {
		t.Errorf("audit log should contain action 'drain', got: %s", output)
	}
}

func TestAuditLogForMutations(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	// Log various mutations.
	al.LogMutation("add_backend", "success", "tok1", &role{
		token: "tok1", permissions: map[string]bool{PermBackends: true},
	})
	al.LogMutation("remove_backend", "success", "tok2", &role{
		token: "tok2", permissions: map[string]bool{PermBackends: true},
	})
	al.LogMutation("restart", "success", "tok3", &role{
		token: "tok3", permissions: map[string]bool{PermRestart: true},
	})

	output := buf.String()

	// Verify all actions appear.
	actions := []string{"add_backend", "remove_backend", "restart"}
	for _, action := range actions {
		if !strings.Contains(output, action) {
			t.Errorf("audit log missing action %q", action)
		}
	}

	// Verify tokens are redacted.
	if strings.Contains(output, "tok1") || strings.Contains(output, "tok2") || strings.Contains(output, "tok3") {
		t.Errorf("audit log contains raw token: %s", output)
	}
}

func TestTokenRedact(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-secret-token-abc", "...-abc"},
		{"short", "...hort"},
		{"12345678", "...5678"},
	}
	for _, tt := range tests {
		got := tokenRedact(tt.input)
		if got != tt.want {
			t.Errorf("tokenRedact(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
