// Modified by [Aleksander R], 2026: PR #2 — path traversal fixes
//
// Regression tests for path traversal hardening:
//   - DELETE /video/<name> and DELETE /logs/<name> reject `..` payloads
//   - file upload (zip-slip) rejects entries with traversal names
//   - new files inside output dirs are still served correctly

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
	"testing"

	assert "github.com/stretchr/testify/require"
)

// TestDeleteVideoRejectsTraversal verifies DELETE /video/<...> with a
// traversal payload returns 400 and does NOT remove anything outside the
// configured videoOutputDir.
//
// Note on encoding: the literal path "/video/../../canary" is cleaned by
// net/http on the client side before transmission, so it never reaches the
// handler with traversal segments intact. We URL-encode `..` as `%2E%2E`
// (and `/` as `%2F`) so the segments survive the client-side cleanup —
// this mirrors how a real attacker's curl/wget/script would shape the
// request to defeat naive proxies.
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

// TestDeleteLogsRejectsTraversal — symmetric guard for the logs endpoint.
// See TestDeleteVideoRejectsTraversal for the URL-encoding rationale.
//
// The logs handler in logs_ws.go only routes to deleteFileIfExists when the
// path component ends with the log file extension (.log) — otherwise it
// upgrades to a WebSocket. The canary file therefore ends in `.log` so the
// DELETE branch actually runs and exercises the safepath check.
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

// TestFileUploadRejectsZipSlip verifies a zip entry whose Name contains
// traversal segments is rejected before any byte hits the filesystem.
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

	// Construct a zip whose single entry tries to escape /tmp/<sid>/.
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

	// Confirm nothing escaped to the host filesystem.
	_, statErr := os.Stat("/tmp/selenwright-pwn.txt")
	assert.True(t, os.IsNotExist(statErr), "no file should have been written outside upload dir")
}

// TestFileUploadHonestEntryStillWorks — regression: legit upload paths
// continue to work after the safepath guard is added.
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

// TestOutputDirsHaveExecuteBit ensures the H3 fix sticks: video/log dirs
// must be enterable so file listing and per-file Stat work for non-root.
func TestOutputDirsHaveExecuteBit(t *testing.T) {
	for _, dir := range []string{app.videoOutputDir, app.logOutputDir} {
		info, err := os.Stat(dir)
		assert.NoError(t, err, "test setup must create %s", dir)
		mode := info.Mode().Perm()
		assert.NotZero(t, mode&0o100,
			"output dir %s mode %#o is missing owner execute bit", dir, mode)
	}
}

// --- helpers ---------------------------------------------------------------

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
