package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	assert "github.com/stretchr/testify/require"
)

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
