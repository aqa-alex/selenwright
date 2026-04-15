package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	assert "github.com/stretchr/testify/require"
)

// TestNoDeprecatedImports guards against reintroducing archived/deprecated
// dependencies: github.com/imdario/mergo (archived, superseded by
// dario.cat/mergo), github.com/pkg/errors (archived since 2021, stdlib
// errors + fmt.Errorf("%w") cover the API), and golang.org/x/net/websocket
// (deprecated, gorilla/websocket is the successor). Transitive/indirect
// references in go.sum are acceptable — only direct imports in .go files
// are scanned. Build a token from fragments so this file doesn't match
// itself.
func TestNoDeprecatedImports(t *testing.T) {
	slash := "/"
	quote := `"`
	banned := []string{
		quote + "github.com" + slash + "imdario" + slash + "mergo" + quote,
		quote + "github.com" + slash + "pkg" + slash + "errors" + quote,
		quote + "golang.org" + slash + "x" + slash + "net" + slash + "websocket" + quote,
	}
	repoRoot, err := os.Getwd()
	assert.NoError(t, err)

	var offenders []string
	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".agents" || name == "video" || name == "logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(data)
		for _, b := range banned {
			if strings.Contains(src, b) {
				offenders = append(offenders, path+" imports "+b)
			}
		}
		return nil
	})
	assert.NoError(t, err)
	assert.Empty(t, offenders, "deprecated imports reintroduced")
}

// TestNoLegacyOriginGateWrap locks in PR #14's removal of the gateOrigin
// wrapper around VNC and Logs endpoints. The gorilla upgrader's CheckOrigin
// now enforces the allow-list directly; reintroducing the wrap would signal
// a regression toward the pre-PR-14 two-layer defense.
func TestNoLegacyOriginGateWrap(t *testing.T) {
	repoRoot, err := os.Getwd()
	assert.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repoRoot, "main.go"))
	assert.NoError(t, err)
	src := string(data)
	assert.NotContains(t, src, "gateOrigin(websocket.Handler",
		"legacy wrap gateOrigin(websocket.Handler ...) must not return")
	assert.NotContains(t, src, "gateOrigin(gateSessionOwner",
		"legacy layered wrap gateOrigin(gateSessionOwner(...)) must not return")
}
