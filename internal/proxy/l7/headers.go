package l7

import (
	"net/http"
	"strings"
)

// hopByHopHeaders is the set of HTTP headers that must not be
// forwarded by a proxy, per RFC 9110 Section 7.6.1. These are
// meaningful only for a single hop and must be stripped before
// forwarding.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"TE":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// StripHopByHop removes hop-by-hop headers from the request. It
// strips both the standard set defined in hopByHopHeaders and any
// additional headers listed in the Connection header's comma-separated
// token list, per RFC 9110 Section 7.6.1. The Connection header
// itself is also removed.
func StripHopByHop(h http.Header) {
	if conn := h.Get("Connection"); conn != "" {
		for _, token := range strings.Split(conn, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				h.Del(token)
			}
		}
		h.Del("Connection")
	}

	for header := range hopByHopHeaders {
		h.Del(header)
	}
}

// InjectForwardedHeaders sets X-Real-IP and appends to X-Forwarded-For
// on the outbound request. X-Forwarded-For is appended to any existing
// value rather than overwriting, since this proxy may itself sit
// behind another proxy that already set the header. Existing values
// are sanitised to prevent header injection: \r and \n characters are
// stripped from each token before appending.
func InjectForwardedHeaders(req *http.Request, clientIP string) {
	req.Header.Set("X-Real-IP", clientIP)

	if existing := req.Header.Get("X-Forwarded-For"); existing != "" {
		sanitised := sanitiseXFF(existing)
		req.Header.Set("X-Forwarded-For", sanitised+", "+clientIP)
	} else {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
}

// sanitiseXFF strips \r and \n from an X-Forwarded-For value to
// prevent header injection / response splitting attacks. Individual
// tokens are preserved as-is otherwise to maintain legitimate proxy
// chain semantics per RFC 7239.
func sanitiseXFF(val string) string {
	// Fast path: if no CR or LF, return as-is.
	if !strings.ContainsAny(val, "\r\n") {
		return val
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(val)
}
