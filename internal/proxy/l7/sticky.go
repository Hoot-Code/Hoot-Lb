package l7

import (
	"net/http"
	"net/url"
	"time"
)

// PoolMembershipCheck is a function that reports whether the given
// address is currently a healthy member of a specific pool. It is
// called per-request to validate sticky cookies against live pool
// state, and is designed to support future hot-reload scenarios
// where pool membership may change without a process restart.
type PoolMembershipCheck func(addr string) bool

// validateStickyCookie extracts and validates a sticky cookie from the
// request. It returns the backend address if the cookie exists, is
// parseable, and references a backend that is currently in the pool
// and healthy. Returns empty string for any invalid/missing cookie —
// the caller should fall back to normal Pick.
func validateStickyCookie(cookie *http.Cookie, check PoolMembershipCheck) string {
	if cookie == nil || cookie.Value == "" {
		return ""
	}
	addr, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return ""
	}
	if check == nil {
		return ""
	}
	if check(addr) {
		return addr
	}
	return ""
}

// setStickyCookie adds a Set-Cookie header to the given response
// headers for the given backend address, with security attributes:
// Secure, HttpOnly, SameSite=Lax, Path=/, and Max-Age matching the
// configured TTL. The headers parameter is http.Header, which is the
// same type returned by both http.ResponseWriter.Header() and
// *http.Response.Header, making this usable from both normal handler
// contexts and ModifyResponse callbacks.
func setStickyCookie(headers http.Header, name string, backendAddr string, ttl time.Duration, tlsTerminated bool) {
	c := &http.Cookie{
		Name:     name,
		Value:    url.QueryEscape(backendAddr),
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Secure:   tlsTerminated,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	headers.Add("Set-Cookie", c.String())
}
