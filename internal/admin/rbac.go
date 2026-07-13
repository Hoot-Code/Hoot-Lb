package admin

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// permission constants for endpoint authorization.
const (
	PermRead     = "read"
	PermDrain    = "drain"
	PermRestart  = "restart"
	PermBackends = "backends"
	PermConfig   = "config"
)

// role represents a resolved role with its token and permissions.
type role struct {
	token       string
	permissions map[string]bool
}

// rbacMiddleware enforces role-based access control. When roles are
// configured, each request's bearer token is matched against the
// configured roles and the endpoint's required permission is checked.
func rbacMiddleware(roles []role, next http.Handler) http.Handler {
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, `{"error":"missing or invalid Authorization header"}`, http.StatusUnauthorized)
			return
		}
		got := strings.TrimPrefix(auth, "Bearer ")
		if got == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, `{"error":"missing or invalid Authorization header"}`, http.StatusUnauthorized)
			return
		}

		// Find the matching role.
		var matched *role
		for i := range roles {
			if subtle.ConstantTimeCompare([]byte(got), []byte(roles[i].token)) == 1 {
				matched = &roles[i]
				break
			}
		}

		if matched == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}

		// Check permission for this endpoint.
		perm := endpointPermission(r)
		if perm != "" && !matched.permissions[perm] {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		// Store role info for audit logging.
		ctx := context.WithValue(r.Context(), roleContextKey{}, matched)
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
}

// endpointPermission returns the required permission for the given
// request's endpoint.
func endpointPermission(r *http.Request) string {
	path := r.URL.Path
	method := r.Method

	switch {
	case path == "/admin/pools" && method == http.MethodGet:
		return PermRead
	case strings.HasPrefix(path, "/admin/pools/") && strings.HasSuffix(path, "/drain"):
		return PermDrain
	case strings.HasPrefix(path, "/admin/pools/") && strings.HasSuffix(path, "/undrain"):
		return PermDrain
	case path == "/admin/restart":
		return PermRestart
	case strings.HasPrefix(path, "/admin/pools/") && strings.HasSuffix(path, "/backends") && method == http.MethodPost:
		return PermBackends
	case strings.Contains(path, "/backends/") && method == http.MethodDelete:
		return PermBackends
	default:
		return ""
	}
}

// roleContextKey is the context key for the matched role.
type roleContextKey struct{}

// getRole returns the matched role from the request context.
func getRole(r *http.Request) *role {
	role, _ := r.Context().Value(roleContextKey{}).(*role)
	return role
}

// tokenRedact returns a redacted form of the token for audit logging.
// Shows only the last 4 characters, never the full token.
func tokenRedact(token string) string {
	if len(token) <= 4 {
		return "****"
	}
	return "..." + token[len(token)-4:]
}
