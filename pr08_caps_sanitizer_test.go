package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

func withCapsPolicy(t *testing.T, policy string) {
	t.Helper()
	prev := capsPolicyFlag
	capsPolicyFlag = policy
	t.Cleanup(func() { capsPolicyFlag = prev })
}

func TestCapsSanitizer_StrictRejectsDnsServersForNonAdmin(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","dnsServers":["8.8.8.8"]}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var msg map[string]interface{}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&msg))
	val, _ := msg["value"].(map[string]interface{})
	assert.Contains(t, fmt.Sprintf("%v", val["message"]), "dnsServers")
}

func TestCapsSanitizer_StrictRejectsEnv(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","env":["LD_PRELOAD=/tmp/x"]}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCapsSanitizer_AdminBypassesStrict(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", AdminHeader: "X-Admin"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","env":["FOO=bar"]}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "root")
	req.Header.Set("X-Admin", "true")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"admin must be allowed to set env caps")

	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	sessions.Remove(sess["sessionId"])
	queue.Release()
}

func TestCapsSanitizer_PermissivePassesEverything(t *testing.T) {
	withCapsPolicy(t, "permissive")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","env":["FOO=bar"],"dnsServers":["8.8.8.8"]}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"permissive mode must let dangerous caps through (legacy behavior)")

	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	sessions.Remove(sess["sessionId"])
	queue.Release()
}

func TestCapsSanitizer_RejectsTraversalInVideoName(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","videoName":"../escape.mp4"}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCapsSanitizer_RejectsCRLFInTestName(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","name":"evil\ninjection"}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
