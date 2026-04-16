package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aqa-alex/selenwright/config"
	"github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fake ImageLister ---

type fakeImageLister struct {
	images []image.Summary
}

func (f *fakeImageLister) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return f.images, nil
}

// --- ParseLabels ---

func TestParseLabels_PerField(t *testing.T) {
	labels := map[string]string{
		LabelBrowser:  "chrome",
		LabelVersion:  "121.0",
		LabelPort:     "4444",
		LabelProtocol: "webdriver",
		LabelPath:     "/wd/hub",
		LabelDefault:  "true",
		LabelTmpfs:    `{"/tmp":"size=512m"}`,
		LabelEnv:      `["DISPLAY=:99"]`,
		LabelShmSize:  "268435456",
		LabelMem:      "1g",
		LabelCPU:      "0.5",
	}

	browser, name, version, isDefault, err := ParseLabels(labels)
	require.NoError(t, err)
	assert.Equal(t, "chrome", name)
	assert.Equal(t, "121.0", version)
	assert.True(t, isDefault)
	assert.Equal(t, "4444", browser.Port)
	assert.Equal(t, "/wd/hub", browser.Path)
	assert.Equal(t, "webdriver", browser.Protocol)
	assert.Equal(t, map[string]string{"/tmp": "size=512m"}, browser.Tmpfs)
	assert.Equal(t, []string{"DISPLAY=:99"}, browser.Env)
	assert.Equal(t, int64(268435456), browser.ShmSize)
	assert.Equal(t, "1g", browser.Mem)
	assert.Equal(t, "0.5", browser.Cpu)
}

func TestParseLabels_ConfigEscapeHatch(t *testing.T) {
	labels := map[string]string{
		LabelBrowser: "firefox",
		LabelVersion: "102.0",
		LabelPort:    "9999",
		LabelConfig:  `{"port":"4444","path":"/wd/hub","protocol":"webdriver","tmpfs":{"/tmp":"size=256m"}}`,
	}

	browser, name, version, _, err := ParseLabels(labels)
	require.NoError(t, err)
	assert.Equal(t, "firefox", name)
	assert.Equal(t, "102.0", version)
	assert.Equal(t, "4444", browser.Port)
	assert.Equal(t, "/wd/hub", browser.Path)
	assert.Equal(t, map[string]string{"/tmp": "size=256m"}, browser.Tmpfs)
}

func TestParseLabels_MissingBrowser(t *testing.T) {
	labels := map[string]string{
		LabelVersion: "1.0",
		LabelPort:    "4444",
	}
	_, _, _, _, err := ParseLabels(labels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), LabelBrowser)
}

func TestParseLabels_MissingVersion(t *testing.T) {
	labels := map[string]string{
		LabelBrowser: "chrome",
		LabelPort:    "4444",
	}
	_, _, _, _, err := ParseLabels(labels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), LabelVersion)
}

func TestParseLabels_MissingPort(t *testing.T) {
	labels := map[string]string{
		LabelBrowser: "chrome",
		LabelVersion: "121.0",
	}
	_, _, _, _, err := ParseLabels(labels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), LabelPort)
}

func TestParseLabels_DefaultFalseWhenAbsent(t *testing.T) {
	labels := map[string]string{
		LabelBrowser: "chrome",
		LabelVersion: "121.0",
		LabelPort:    "4444",
	}
	_, _, _, isDefault, err := ParseLabels(labels)
	require.NoError(t, err)
	assert.False(t, isDefault)
}

// --- ScanImages ---

func TestScanImages_BasicDiscovery(t *testing.T) {
	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:          "sha256:aaa",
			RepoTags:    []string{"selenwright/chrome:121.0"},
			RepoDigests: []string{"selenwright/chrome@sha256:aaa111"},
			Created:     1000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
				LabelDefault: "true",
			},
		},
	}}

	result, err := ScanImages(context.Background(), lister)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "chrome", result[0].Name)
	assert.Equal(t, "121.0", result[0].Version)
	assert.True(t, result[0].Default)
	assert.Equal(t, "selenwright/chrome@sha256:aaa111", result[0].Digest)
	assert.Equal(t, "selenwright/chrome:121.0", result[0].Browser.Image)
}

func TestScanImages_DedupByBrowserVersion(t *testing.T) {
	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:          "sha256:old",
			RepoTags:    []string{"selenwright/chrome:121.0"},
			RepoDigests: []string{"selenwright/chrome@sha256:old111"},
			Created:     1000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
			},
		},
		{
			ID:          "sha256:new",
			RepoTags:    []string{"selenwright/chrome:121.0-v2"},
			RepoDigests: []string{"selenwright/chrome@sha256:new222"},
			Created:     2000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
			},
		},
	}}

	result, err := ScanImages(context.Background(), lister)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "selenwright/chrome@sha256:new222", result[0].Digest)
}

func TestScanImages_SkipsDanglingImages(t *testing.T) {
	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:       "",
			Created:  1000,
			RepoTags: nil,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
			},
		},
	}}

	result, err := ScanImages(context.Background(), lister)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestScanImages_SkipsMalformedLabels(t *testing.T) {
	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:          "sha256:bad",
			RepoDigests: []string{"x@sha256:bad"},
			Created:     1000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				// missing version and port
			},
		},
	}}

	result, err := ScanImages(context.Background(), lister)
	require.NoError(t, err)
	assert.Empty(t, result)
}

// --- AdoptedStore ---

func TestAdoptedStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Adopt("sha256:aaa", "chrome", "121.0", "selenwright/chrome:121.0"))
	assert.True(t, store.IsAdopted("sha256:aaa"))
	assert.False(t, store.IsDismissed("sha256:aaa"))

	require.NoError(t, store.Dismiss("sha256:bbb", "firefox", "102.0", "selenwright/firefox:102.0"))
	assert.True(t, store.IsDismissed("sha256:bbb"))
	assert.False(t, store.IsAdopted("sha256:bbb"))

	// Reload from disk
	store2, err := NewAdoptedStore(dir)
	require.NoError(t, err)
	assert.True(t, store2.IsAdopted("sha256:aaa"))
	assert.True(t, store2.IsDismissed("sha256:bbb"))
}

func TestAdoptedStore_AdoptOverwritesDismiss(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Dismiss("sha256:aaa", "chrome", "121.0", "selenwright/chrome:121.0"))
	assert.True(t, store.IsDismissed("sha256:aaa"))

	require.NoError(t, store.Adopt("sha256:aaa", "chrome", "121.0", "selenwright/chrome:121.0"))
	assert.True(t, store.IsAdopted("sha256:aaa"))
	assert.False(t, store.IsDismissed("sha256:aaa"))
}

func TestAdoptedStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Adopt("sha256:aaa", "chrome", "121.0", "x"))

	_, err = os.Stat(filepath.Join(dir, adoptedFileName))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, adoptedFileName+".tmp"))
	assert.True(t, os.IsNotExist(err))
}

func TestAdoptedStore_AdoptedDigests(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Adopt("sha256:aaa", "chrome", "121.0", "x"))
	require.NoError(t, store.Adopt("sha256:bbb", "firefox", "102.0", "y"))

	digests := store.AdoptedDigests()
	assert.Len(t, digests, 2)
	assert.Contains(t, digests, "sha256:aaa")
	assert.Contains(t, digests, "sha256:bbb")
}

// --- AssembleCatalog ---

func TestAssembleCatalog_LabelsWin(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)
	require.NoError(t, store.Adopt("selenwright/chrome@sha256:aaa", "chrome", "121.0", "selenwright/chrome:121.0"))

	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:          "sha256:aaa",
			RepoTags:    []string{"selenwright/chrome:121.0"},
			RepoDigests: []string{"selenwright/chrome@sha256:aaa"},
			Created:     1000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
				LabelDefault: "true",
			},
		},
	}}

	cfg := config.NewConfig()
	err = AssembleCatalog(context.Background(), lister, store, cfg, "", "")
	require.NoError(t, err)

	browser, version, found := cfg.Find("chrome", "")
	require.True(t, found)
	assert.Equal(t, "121.0", version)
	assert.Equal(t, "4444", browser.Port)
}

func TestAssembleCatalog_FallbackToJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)

	jsonPath := filepath.Join(dir, "browsers.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{
		"firefox": {
			"default": "102.0",
			"versions": {
				"102.0": {"image": "selenwright/firefox:102.0", "port": "4444"}
			}
		}
	}`), 0o644))

	lister := &fakeImageLister{images: nil}
	cfg := config.NewConfig()
	err = AssembleCatalog(context.Background(), lister, store, cfg, jsonPath, "")
	require.NoError(t, err)

	browser, version, found := cfg.Find("firefox", "")
	require.True(t, found)
	assert.Equal(t, "102.0", version)
	assert.Equal(t, "4444", browser.Port)
}

func TestAssembleCatalog_EmptyActiveSetFallsBackToJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)
	// Images exist on host but none are adopted.

	jsonPath := filepath.Join(dir, "browsers.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{
		"chrome": {
			"default": "120.0",
			"versions": {
				"120.0": {"image": "selenwright/chrome:120.0", "port": "4444"}
			}
		}
	}`), 0o644))

	lister := &fakeImageLister{images: []image.Summary{
		{
			ID:          "sha256:unadopted",
			RepoTags:    []string{"selenwright/chrome:121.0"},
			RepoDigests: []string{"selenwright/chrome@sha256:unadopted"},
			Created:     1000,
			Labels: map[string]string{
				LabelBrowser: "chrome",
				LabelVersion: "121.0",
				LabelPort:    "4444",
			},
		},
	}}

	cfg := config.NewConfig()
	err = AssembleCatalog(context.Background(), lister, store, cfg, jsonPath, "")
	require.NoError(t, err)

	// Should get JSON version, not the unadopted label version
	browser, version, found := cfg.Find("chrome", "")
	require.True(t, found)
	assert.Equal(t, "120.0", version)
	assert.Equal(t, "4444", browser.Port)
}

// --- FilterUnadopted ---

func TestFilterUnadopted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAdoptedStore(dir)
	require.NoError(t, err)
	require.NoError(t, store.Adopt("sha256:adopted", "chrome", "121.0", "x"))
	require.NoError(t, store.Dismiss("sha256:dismissed", "firefox", "102.0", "y"))

	discovered := []DiscoveredImage{
		{Digest: "sha256:adopted", Name: "chrome", Version: "121.0"},
		{Digest: "sha256:dismissed", Name: "firefox", Version: "102.0"},
		{Digest: "sha256:new", Name: "edge", Version: "100.0"},
	}

	unadopted := FilterUnadopted(discovered, store)
	require.Len(t, unadopted, 1)
	assert.Equal(t, "sha256:new", unadopted[0].Digest)
}

// --- compareVersions ---

func TestCompareVersions(t *testing.T) {
	assert.True(t, compareVersions("1.0", "2.0") < 0)
	assert.True(t, compareVersions("2.0", "1.0") > 0)
	assert.True(t, compareVersions("1.0", "1.0") == 0)
	assert.True(t, compareVersions("1.9", "1.10") < 0)
	assert.True(t, compareVersions("121.0", "122.0") < 0)
	assert.True(t, compareVersions("1.49.0", "1.56.1") < 0)
}
