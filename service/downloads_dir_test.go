package service

import (
	"testing"

	"github.com/aqa-alex/selenwright/config"
	"github.com/stretchr/testify/require"
)

func TestGetDownloadsDir_EmptyWhenEnvMissing(t *testing.T) {
	require.Equal(t, "", getDownloadsDir(&config.Browser{}))
}

func TestGetDownloadsDir_EmptyWhenNilBrowser(t *testing.T) {
	require.Equal(t, "", getDownloadsDir(nil))
}

func TestGetDownloadsDir_ReadsFromEnvEntry(t *testing.T) {
	b := &config.Browser{Env: []string{"SELENWRIGHT_DOWNLOADS_DIR=/home/pwuser/Downloads"}}
	require.Equal(t, "/home/pwuser/Downloads", getDownloadsDir(b))
}

func TestGetDownloadsDir_IgnoresOtherEntries(t *testing.T) {
	b := &config.Browser{Env: []string{"TZ=UTC", "SELENWRIGHT_DOWNLOADS_DIR=/var/tmp/dl", "OTHER=x"}}
	require.Equal(t, "/var/tmp/dl", getDownloadsDir(b))
}

func TestGetDownloadsDir_EmptyValueReturnsEmpty(t *testing.T) {
	b := &config.Browser{Env: []string{"SELENWRIGHT_DOWNLOADS_DIR="}}
	require.Equal(t, "", getDownloadsDir(b))
}
