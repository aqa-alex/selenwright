package protect

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type OriginChecker struct {
	allowed map[string]struct{}
	star    bool
}

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

func (c *OriginChecker) AllowsAll() bool {
	return c.star && len(c.allowed) == 0
}

func (c *OriginChecker) Check(r *http.Request) bool {
	if c == nil {
		return false
	}
	if c.star {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	canonical, err := canonicalOrigin(origin)
	if err != nil {
		return false
	}
	_, ok := c.allowed[canonical]
	return ok
}

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
