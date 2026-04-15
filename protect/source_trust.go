package protect

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

const HeaderRouterSecret = "X-Router-Secret"

var ErrUntrustedSource = errors.New("request did not come from a trusted source")

type SourceTrust struct {
	mu             sync.RWMutex
	secret         string
	trustedCIDRs   []*net.IPNet
	requireMTLS    bool
	allowedRootCAs *x509.CertPool
	stripHeaders   []string
	enabled        bool
}

type SourceTrustConfig struct {
	Secret         string
	TrustedCIDRs   []*net.IPNet
	RequireMTLS    bool
	AllowedRootCAs *x509.CertPool
	StripHeaders   []string
}

func NewSourceTrust(cfg SourceTrustConfig) *SourceTrust {
	st := &SourceTrust{stripHeaders: cfg.StripHeaders}
	st.Update(cfg)
	return st
}

func (st *SourceTrust) Update(cfg SourceTrustConfig) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.secret = cfg.Secret
	st.trustedCIDRs = cfg.TrustedCIDRs
	st.requireMTLS = cfg.RequireMTLS
	st.allowedRootCAs = cfg.AllowedRootCAs
	if cfg.StripHeaders != nil {
		st.stripHeaders = cfg.StripHeaders
	}
	st.enabled = cfg.Secret != "" || len(cfg.TrustedCIDRs) > 0 || cfg.RequireMTLS
}

func (st *SourceTrust) Enabled() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.enabled
}

func (st *SourceTrust) Check(r *http.Request) error {
	st.mu.RLock()
	secret := st.secret
	cidrs := st.trustedCIDRs
	requireMTLS := st.requireMTLS
	caPool := st.allowedRootCAs
	st.mu.RUnlock()

	if secret != "" {
		if !ConstantTimeStringEqual(r.Header.Get(HeaderRouterSecret), secret) {
			return fmt.Errorf("%w: missing or invalid %s", ErrUntrustedSource, HeaderRouterSecret)
		}
	}
	if len(cidrs) > 0 {
		ip := remoteIP(r)
		if ip == nil || !ipInAny(ip, cidrs) {
			return fmt.Errorf("%w: source %s not in trusted CIDR list", ErrUntrustedSource, r.RemoteAddr)
		}
	}
	if requireMTLS {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			return fmt.Errorf("%w: client certificate required", ErrUntrustedSource)
		}
		if caPool != nil {
			leaf := r.TLS.VerifiedChains[0][0]
			opts := x509.VerifyOptions{Roots: caPool, Intermediates: x509.NewCertPool()}
			for _, chain := range r.TLS.VerifiedChains {
				for _, cert := range chain[1:] {
					opts.Intermediates.AddCert(cert)
				}
			}
			if _, err := leaf.Verify(opts); err != nil {
				return fmt.Errorf("%w: client cert not trusted: %v", ErrUntrustedSource, err)
			}
		}
	}
	return nil
}

func (st *SourceTrust) StripFromRequest(r *http.Request) {
	st.mu.RLock()
	headers := st.stripHeaders
	st.mu.RUnlock()
	for _, h := range headers {
		r.Header.Del(h)
	}
}

func SourceTrustMiddleware(stFn func() *SourceTrust, openPaths []string) func(http.Handler) http.Handler {
	return SourceTrustMiddlewareWithHooks(stFn, openPaths, nil)
}

func SourceTrustMiddlewareWithHooks(stFn func() *SourceTrust, openPaths []string, onFailure func()) func(http.Handler) http.Handler {
	openExact := make(map[string]struct{}, len(openPaths))
	for _, p := range openPaths {
		openExact[p] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := openExact[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			st := stFn()
			if st == nil || !st.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			if err := st.Check(r); err != nil {
				if onFailure != nil {
					onFailure()
				}
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ParseCIDRs(in []string) ([]*net.IPNet, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", raw, err)
		}
		out = append(out, network)
	}
	return out, nil
}

func ipInAny(ip net.IP, cidrs []*net.IPNet) bool {
	for _, c := range cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
