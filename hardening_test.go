package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

func TestCreateBodyTooLarge(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	oversized := bytes.Repeat([]byte("x"), int(app.maxCreateBodyBytes)+1)
	resp, err := http.Post(
		With(srv.URL).Path("/wd/hub/session"),
		"application/json",
		bytes.NewReader(oversized),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"oversized create body must be rejected with 400")
	assert.Equal(t, 0, app.queue.Used(),
		"oversized body must drop the queue slot")
}

func TestFileUploadBodyTooLarge(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		app.sessions.Remove(sess["sessionId"])
		app.queue.Release()
	}()

	original := app.maxUploadBodyBytes
	app.maxUploadBodyBytes = 1 << 10 // 1 KiB
	defer func() { app.maxUploadBodyBytes = original }()

	envelope := []byte(`{"file":"` + strings.Repeat("A", 4*1024) + `"}`)
	resp, err = http.Post(
		With(srv.URL).Path(fmt.Sprintf("/wd/hub/session/%s/file", sess["sessionId"])),
		"application/json",
		bytes.NewReader(envelope),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"oversized upload body must not succeed")
}

func TestFileUploadZipBombDeclaredSize(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		app.sessions.Remove(sess["sessionId"])
		app.queue.Release()
	}()

	originalExtracted := app.maxUploadExtractedBytes
	app.maxUploadExtractedBytes = 16
	defer func() { app.maxUploadExtractedBytes = originalExtracted }()

	payload := bytes.Repeat([]byte("z"), 32)
	zipBytes := buildZipWithPayload(t, "bomb.txt", payload)

	envelope := mustEncodeFileEnvelope(t, zipBytes)
	resp, err = http.Post(
		With(srv.URL).Path(fmt.Sprintf("/wd/hub/session/%s/file", sess["sessionId"])),
		"application/json",
		bytes.NewReader(envelope),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"declared uncompressed size > limit must be rejected with 400")
}

func TestFileUploadHonestUnderLimit(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		app.sessions.Remove(sess["sessionId"])
		app.queue.Release()
	}()

	zipBytes := buildZipWithPayload(t, "ok.txt", []byte("Hello World!"))
	envelope := mustEncodeFileEnvelope(t, zipBytes)
	resp, err = http.Post(
		With(srv.URL).Path(fmt.Sprintf("/wd/hub/session/%s/file", sess["sessionId"])),
		"application/json",
		bytes.NewReader(envelope),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"file under limit must still upload successfully")
}

func TestServerTimeoutsConfigured(t *testing.T) {
	assert.Greater(t, int64(readHeaderTimeout), int64(0),
		"readHeaderTimeout must be set to defend against Slowloris")
	assert.Greater(t, int64(readTimeout), int64(0),
		"readTimeout must bound non-streaming request body reads")
	assert.Greater(t, int64(idleTimeout), int64(0),
		"idleTimeout must close idle keep-alive sockets")
	assert.Greater(t, maxHeaderBytes, 0,
		"maxHeaderBytes must cap request header size")
}

func TestDeleteVideoRejectsTraversal(t *testing.T) {
	parent := filepath.Dir(filepath.Dir(app.videoOutputDir))
	canary := filepath.Join(parent, "selenwright-canary.txt")
	assert.NoError(t, os.WriteFile(canary, []byte("canary"), 0o600))
	t.Cleanup(func() { _ = os.Remove(canary) })

	encodedTraversal := "%2E%2E%2F%2E%2E%2F" + filepath.Base(canary)

	req, err := http.NewRequest(http.MethodDelete, srv.URL+paths.Video+encodedTraversal, nil)
	assert.NoError(t, err)
	rsp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer rsp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, rsp.StatusCode,
		"DELETE with traversal must be rejected with 400")

	_, statErr := os.Stat(canary)
	assert.NoError(t, statErr, "canary file must still exist after rejected DELETE")
}

func TestDeleteLogsRejectsTraversal(t *testing.T) {
	parent := filepath.Dir(filepath.Dir(app.logOutputDir))
	canary := filepath.Join(parent, "selenwright-log-canary.log")
	assert.NoError(t, os.WriteFile(canary, []byte("canary"), 0o600))
	t.Cleanup(func() { _ = os.Remove(canary) })

	encodedTraversal := "%2E%2E%2F%2E%2E%2F" + filepath.Base(canary)

	req, err := http.NewRequest(http.MethodDelete, srv.URL+paths.Logs+encodedTraversal, nil)
	assert.NoError(t, err)
	rsp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer rsp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, rsp.StatusCode)
	_, statErr := os.Stat(canary)
	assert.NoError(t, statErr)
}

func TestFileUploadRejectsZipSlip(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	t.Cleanup(func() {
		app.sessions.Remove(sess["sessionId"])
		app.queue.Release()
	})

	zipBytes := buildZipWithName(t, "../../../tmp/selenwright-pwn.txt", []byte("pwn"))
	envelope := []byte(fmt.Sprintf(`{"file":"%s"}`, base64.StdEncoding.EncodeToString(zipBytes)))

	resp, err = http.Post(
		With(srv.URL).Path(fmt.Sprintf("/wd/hub/session/%s/file", sess["sessionId"])),
		"application/json",
		bytes.NewReader(envelope),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"zip entry with traversal name must be rejected")

	_, statErr := os.Stat("/tmp/selenwright-pwn.txt")
	assert.True(t, os.IsNotExist(statErr), "no file should have been written outside upload dir")
}

func TestFileUploadHonestEntryStillWorks(t *testing.T) {
	app.manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	t.Cleanup(func() {
		app.sessions.Remove(sess["sessionId"])
		app.queue.Release()
	})

	zipBytes := buildZipWithName(t, "ok.txt", []byte("Hello"))
	envelope := []byte(fmt.Sprintf(`{"file":"%s"}`, base64.StdEncoding.EncodeToString(zipBytes)))

	resp, err = http.Post(
		With(srv.URL).Path(fmt.Sprintf("/wd/hub/session/%s/file", sess["sessionId"])),
		"application/json",
		bytes.NewReader(envelope),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestOutputDirsHaveExecuteBit(t *testing.T) {
	for _, dir := range []string{app.videoOutputDir, app.logOutputDir} {
		info, err := os.Stat(dir)
		assert.NoError(t, err, "test setup must create %s", dir)
		mode := info.Mode().Perm()
		assert.NotZero(t, mode&0o100,
			"output dir %s mode %#o is missing owner execute bit", dir, mode)
	}
}

func withCapsPolicy(t *testing.T, policy string) {
	t.Helper()
	prev := app.capsPolicyFlag
	app.capsPolicyFlag = policy
	t.Cleanup(func() { app.capsPolicyFlag = prev })
}

func TestCapsSanitizer_StrictRejectsDnsServersForNonAdmin(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	app.manager = &HTTPTest{Handler: Selenium()}

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
	app.manager = &HTTPTest{Handler: Selenium()}

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
	app.manager = &HTTPTest{Handler: Selenium()}

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
	app.sessions.Remove(sess["sessionId"])
	app.queue.Release()
}

func TestCapsSanitizer_PermissivePassesEverything(t *testing.T) {
	withCapsPolicy(t, "permissive")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	app.manager = &HTTPTest{Handler: Selenium()}

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
	app.sessions.Remove(sess["sessionId"])
	app.queue.Release()
}

func TestCapsSanitizer_RejectsTraversalInVideoName(t *testing.T) {
	withCapsPolicy(t, "strict")
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})
	app.manager = &HTTPTest{Handler: Selenium()}

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
	app.manager = &HTTPTest{Handler: Selenium()}

	body := []byte(`{"desiredCapabilities":{"browserName":"firefox","name":"evil\ninjection"}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wd/hub/session", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func buildZipWithPayload(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	assert.NoError(t, err)
	_, err = w.Write(payload)
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())
	return buf.Bytes()
}

func mustEncodeFileEnvelope(t *testing.T, raw []byte) []byte {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(raw)
	return []byte(fmt.Sprintf(`{"file":"%s"}`, encoded))
}

func buildZipWithName(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	assert.NoError(t, err)
	_, err = w.Write(payload)
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())
	return buf.Bytes()
}
