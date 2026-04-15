package protect

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceTrust_DisabledByDefault(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{})
	require.False(t, st.Enabled())
	require.NoError(t, st.Check(makeReq(t, "1.2.3.4:1234", nil)))
}

func TestSourceTrust_SecretRequired(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{Secret: "topsecret"})
	require.True(t, st.Enabled())

	require.ErrorIs(t, st.Check(makeReq(t, "1.2.3.4:1234", nil)), ErrUntrustedSource)
	require.ErrorIs(t, st.Check(makeReq(t, "1.2.3.4:1234", http.Header{HeaderRouterSecret: {"wrong"}})), ErrUntrustedSource)
	require.NoError(t, st.Check(makeReq(t, "1.2.3.4:1234", http.Header{HeaderRouterSecret: {"topsecret"}})))
}

func TestSourceTrust_CIDR(t *testing.T) {
	cidrs, err := ParseCIDRs([]string{"10.0.0.0/8", "192.168.1.0/24"})
	require.NoError(t, err)
	st := NewSourceTrust(SourceTrustConfig{TrustedCIDRs: cidrs})
	require.True(t, st.Enabled())

	require.NoError(t, st.Check(makeReq(t, "10.20.30.40:1234", nil)))
	require.NoError(t, st.Check(makeReq(t, "192.168.1.5:1234", nil)))
	require.ErrorIs(t, st.Check(makeReq(t, "8.8.8.8:1234", nil)), ErrUntrustedSource)
}

func TestSourceTrust_BothSecretAndCIDR_AllRequired(t *testing.T) {
	cidrs, err := ParseCIDRs([]string{"10.0.0.0/8"})
	require.NoError(t, err)
	st := NewSourceTrust(SourceTrustConfig{Secret: "topsecret", TrustedCIDRs: cidrs})

	require.ErrorIs(t, st.Check(makeReq(t, "10.0.0.1:1234", nil)), ErrUntrustedSource)
	require.ErrorIs(t, st.Check(makeReq(t, "8.8.8.8:1234", http.Header{HeaderRouterSecret: {"topsecret"}})), ErrUntrustedSource)
	require.NoError(t, st.Check(makeReq(t, "10.0.0.1:1234", http.Header{HeaderRouterSecret: {"topsecret"}})))
}

func TestSourceTrust_RequireMTLS(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{RequireMTLS: true})

	r := makeReq(t, "10.0.0.1:1234", nil)
	require.ErrorIs(t, st.Check(r), ErrUntrustedSource)

	rWithCert := makeReq(t, "10.0.0.1:1234", nil)
	rWithCert.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	require.NoError(t, st.Check(rWithCert))
}

func TestSourceTrust_StripFromRequest(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{
		Secret:       "x",
		StripHeaders: []string{HeaderRouterSecret, "X-Forwarded-User", "X-Admin"},
	})
	r := makeReq(t, "10.0.0.1:1234", http.Header{
		HeaderRouterSecret: {"x"},
		"X-Forwarded-User": {"alice"},
		"X-Admin":          {"true"},
		"Content-Type":     {"application/json"},
	})
	st.StripFromRequest(r)
	require.Empty(t, r.Header.Get(HeaderRouterSecret))
	require.Empty(t, r.Header.Get("X-Forwarded-User"))
	require.Empty(t, r.Header.Get("X-Admin"))
	require.Equal(t, "application/json", r.Header.Get("Content-Type"))
}

func TestSourceTrust_Update_SwapsConfig(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{Secret: "old"})
	require.NoError(t, st.Check(makeReq(t, "1.1.1.1:1234", http.Header{HeaderRouterSecret: {"old"}})))

	st.Update(SourceTrustConfig{Secret: "new"})
	require.ErrorIs(t, st.Check(makeReq(t, "1.1.1.1:1234", http.Header{HeaderRouterSecret: {"old"}})), ErrUntrustedSource)
	require.NoError(t, st.Check(makeReq(t, "1.1.1.1:1234", http.Header{HeaderRouterSecret: {"new"}})))
}

func TestParseCIDRs(t *testing.T) {
	out, err := ParseCIDRs([]string{"10.0.0.0/8", "  192.168.0.0/16  "})
	require.NoError(t, err)
	require.Len(t, out, 2)

	_, err = ParseCIDRs([]string{"not-a-cidr"})
	require.Error(t, err)

	out, err = ParseCIDRs(nil)
	require.NoError(t, err)
	require.Nil(t, out)
}

func TestErrUntrustedSourceIsRetrievable(t *testing.T) {
	st := NewSourceTrust(SourceTrustConfig{Secret: "x"})
	err := st.Check(makeReq(t, "1.1.1.1:1234", nil))
	require.True(t, errors.Is(err, ErrUntrustedSource))
}

func makeReq(t *testing.T, remoteAddr string, h http.Header) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "/", nil)
	require.NoError(t, err)
	r.RemoteAddr = remoteAddr
	if h != nil {
		r.Header = h.Clone()
	}
	return r
}
