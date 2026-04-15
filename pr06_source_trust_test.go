package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

func withSourceTrust(t *testing.T, st *protect.SourceTrust) {
	t.Helper()
	prev := app.sourceTrust
	app.sourceTrust = st
	t.Cleanup(func() { app.sourceTrust = prev })
}

func TestSourceTrust_RejectsRequestWithoutSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_AcceptsRequestWithCorrectSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set(protect.HeaderRouterSecret, "topsecret")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_RejectsWrongSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set(protect.HeaderRouterSecret, "wrong")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_DisabledByDefault(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{}))

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"empty source-trust config must not block any request")
}

func TestSourceTrust_OpenPathsAreNotGated(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	resp, err := http.Get(srv.URL + paths.Ping)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"open paths still bypass when source-trust is enabled — health-check style endpoints should not require the secret")
}

func TestBuildSourceTrustConfig_StripsRouterHeaders(t *testing.T) {
	cfg, err := buildSourceTrustConfig("trusted-proxy", "secret", "10.0.0.0/8", "", "X-Forwarded-User", "X-Admin")
	assert.NoError(t, err)
	assert.Contains(t, cfg.StripHeaders, protect.HeaderRouterSecret)
	assert.Contains(t, cfg.StripHeaders, "X-Forwarded-User")
	assert.Contains(t, cfg.StripHeaders, "X-Admin")
}

func TestBuildSourceTrustConfig_RejectsInvalidCIDR(t *testing.T) {
	_, err := buildSourceTrustConfig("trusted-proxy", "", "not-a-cidr", "", "", "")
	assert.Error(t, err)
}

func TestBuildSourceTrustConfig_LoadsCAPool(t *testing.T) {
	dir := t.TempDir()
	caPath := dir + "/ca.pem"
	assert.NoError(t, os.WriteFile(caPath, generateTestCAPEM(t), 0o600))

	cfg, err := buildSourceTrustConfig("trusted-proxy", "", "", caPath, "", "")
	assert.NoError(t, err)
	assert.True(t, cfg.RequireMTLS)
	assert.NotNil(t, cfg.AllowedRootCAs)
}

// generateTestCAPEM produces a self-signed CA certificate suitable for the
// PEM loader test. Keeping the generation inline avoids checking in a
// long-lived static test certificate.
func generateTestCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assert.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"selenwright-test-ca"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	assert.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBuildSourceTrustConfig_MissingCAFile(t *testing.T) {
	_, err := buildSourceTrustConfig("trusted-proxy", "", "", "/no/such/file", "", "")
	assert.Error(t, err)
}
