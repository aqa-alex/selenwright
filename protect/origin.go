// Modified by [Aleksander R], 2026: PR #3 — WebSocket CheckOrigin allow-list

package protect

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OriginChecker validates the WebSocket Origin header against a configured
// allow-list. It is intended to replace the `func(_ *http.Request) bool {
// return true }` placeholders in [wsproxy.go] and [playwright.go] that
// expose every WebSocket endpoint to Cross-Site WebSocket Hijacking
// (CSWSH) — any page on the open internet could otherwise hand-shake with
// /devtools/, /playwright/, /vnc/ and /logs/ on a victim instance.
//
// Browser-initiated WebSocket handshakes always carry an Origin header
// reflecting the page that initiated them. Native clients (selenium-go,
// playwright bindings, custom test harnesses) typically omit the header
// entirely; the checker treats an absent Origin as legitimate so those
// callers continue to work unmodified.
type OriginChecker struct {
	// allowed holds the canonical scheme+host origins that pass.
	allowed map[string]struct{}
	// star is true when "*" was supplied — every Origin passes. Only safe
	// when an external network policy or auth layer already gates access.
	star bool
}

// NewOriginChecker builds an OriginChecker from a list of origin strings.
// Entries are normalized (lowercased, default ports stripped) so the
// configuration `https://Ci.Example.Com` matches an actual `https://ci.example.com`
// Origin header without case sensitivity surprises.
//
// A single "*" entry permits any Origin; this is the legacy behavior and
// is dangerous on its own — callers must guard with auth or network
// policy. An empty list also permits any Origin for backward compatibility
// with the previous `CheckOrigin: func() bool { return true }` default,
// but emits a warning at startup (logged by main, not here).
func NewOriginChecker(origins []string) (*OriginChecker, error) {
	c := &OriginChecker{allowed: map[string]struct{}{}}
	if len(origins) == 0 {
		c.star = true
		return c, nil
	}
	for _, raw := range origins {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if raw == "*" {
			c.star = true
			continue
		}
		canonical, err := canonicalOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed origin %q: %w", raw, err)
		}
		c.allowed[canonical] = struct{}{}
	}
	return c, nil
}

// AllowsAll reports whether the checker is in legacy/permissive mode.
// Used by main to emit a startup warning.
func (c *OriginChecker) AllowsAll() bool {
	return c.star && len(c.allowed) == 0
}

// Check is the gorilla/websocket-compatible callback signature. It returns
// true when the request's Origin is acceptable: missing entirely (native
// non-browser client), the literal allow-all marker is configured, or the
// canonicalized Origin appears in the allow-list.
func (c *OriginChecker) Check(r *http.Request) bool {
	if c == nil {
		return false
	}
	if c.star {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Browsers ALWAYS send Origin on WebSocket upgrade; absence
		// indicates a native client (selenium-go, playwright bindings,
		// curl). These cannot be victims of CSWSH because there is no
		// page to host the attacker's JS, so allowing them keeps the
		// existing API ergonomic for non-browser callers.
		return true
	}
	canonical, err := canonicalOrigin(origin)
	if err != nil {
		return false
	}
	_, ok := c.allowed[canonical]
	return ok
}

// canonicalOrigin reduces an origin string to scheme+host(+port) in a
// stable, lowercase form. Default ports for the scheme are removed so
// "https://example.com" and "https://example.com:443" compare equal.
func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("origin must include scheme and host")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !isDefaultPort(scheme, port) {
		host = host + ":" + port
	}
	return scheme + "://" + host, nil
}

func isDefaultPort(scheme, port string) bool {
	switch {
	case scheme == "http" && port == "80":
		return true
	case scheme == "https" && port == "443":
		return true
	case scheme == "ws" && port == "80":
		return true
	case scheme == "wss" && port == "443":
		return true
	}
	return false
}

