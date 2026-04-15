// Modified by [Aleksander R], 2026: PR #1 — server timeouts + body limits
//
// Regression tests for HTTP server hardening:
//   - request body size limits on /session create
//   - request body + extracted size limits on /file upload (zip-bomb guard)
//   - http.Server timeout constants are non-zero where expected

package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	assert "github.com/stretchr/testify/require"
)

// TestCreateBodyTooLarge verifies that posting a request body larger than
// maxCreateBodyBytes is rejected without exhausting memory.
func TestCreateBodyTooLarge(t *testing.T) {
	manager = &HTTPTest{Handler: Selenium()}

	// One byte over the limit — MaxBytesReader will surface an error to
	// io.ReadAll, the handler then returns 400 via jsonerror.InvalidArgument.
	oversized := bytes.Repeat([]byte("x"), int(maxCreateBodyBytes)+1)
	resp, err := http.Post(
		With(srv.URL).Path("/wd/hub/session"),
		"application/json",
		bytes.NewReader(oversized),
	)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"oversized create body must be rejected with 400")
	assert.Equal(t, 0, queue.Used(),
		"oversized body must drop the queue slot")
}

// TestFileUploadBodyTooLarge verifies the upload endpoint rejects oversized
// JSON envelopes before attempting to decode them.
func TestFileUploadBodyTooLarge(t *testing.T) {
	manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		sessions.Remove(sess["sessionId"])
		queue.Release()
	}()

	// Construct a JSON envelope larger than maxUploadBodyBytes. We reuse the
	// existing test default (256 MiB) but temporarily shrink it for the test
	// to avoid allocating a 256 MiB buffer.
	original := maxUploadBodyBytes
	maxUploadBodyBytes = 1 << 10 // 1 KiB
	defer func() { maxUploadBodyBytes = original }()

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

// TestFileUploadZipBombDeclaredSize verifies that a zip whose central
// directory advertises an uncompressed size above the limit is rejected
// before a single decompressed byte is written.
func TestFileUploadZipBombDeclaredSize(t *testing.T) {
	manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		sessions.Remove(sess["sessionId"])
		queue.Release()
	}()

	// Shrink the limit so a tiny test payload can exceed it. The header check
	// uses uint64 comparison — make the limit small and the file slightly
	// larger to trigger rejection without writing a real bomb to disk.
	originalExtracted := maxUploadExtractedBytes
	maxUploadExtractedBytes = 16
	defer func() { maxUploadExtractedBytes = originalExtracted }()

	// 32 bytes of payload — declared and actual size both exceed 16.
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

// TestFileUploadHonestUnderLimit confirms the legitimate path still works
// after the new guards are added.
func TestFileUploadHonestUnderLimit(t *testing.T) {
	manager = &HTTPTest{Handler: Selenium()}

	resp, err := http.Post(With(srv.URL).Path("/wd/hub/session"), "", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var sess map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&sess))
	defer func() {
		sessions.Remove(sess["sessionId"])
		queue.Release()
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

// TestServerTimeoutsConfigured ensures the hardening constants are non-zero —
// guards against a refactor that accidentally drops the &http.Server fields.
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

// --- helpers ---------------------------------------------------------------

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
