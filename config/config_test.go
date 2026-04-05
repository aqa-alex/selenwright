// Modified by [Aleksander R], 2026: added Playwright protocol support

package config

import (
	"testing"

	require "github.com/stretchr/testify/require"
)

func TestConfigFind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		browsers    map[string]Versions
		browserName string
		version     string
		wantVersion string
		wantOK      bool
	}{
		{
			name: "non-playwright prefix match remains unchanged",
			browsers: map[string]Versions{
				"firefox": {
					Versions: map[string]*Browser{
						"49.0": {Protocol: ""},
					},
				},
			},
			browserName: "firefox",
			version:     "49",
			wantVersion: "49.0",
			wantOK:      true,
		},
		{
			name: "playwright exact match wins",
			browsers: map[string]Versions{
				"chromium": {
					Versions: map[string]*Browser{
						"1.49.0": {Protocol: "playwright"},
						"1.49.2": {Protocol: "playwright"},
					},
				},
			},
			browserName: "chromium",
			version:     "1.49.0",
			wantVersion: "1.49.0",
			wantOK:      true,
		},
		{
			name: "playwright patch mismatch matches same major minor",
			browsers: map[string]Versions{
				"chromium": {
					Versions: map[string]*Browser{
						"1.49.0": {Protocol: "playwright"},
					},
				},
			},
			browserName: "chromium",
			version:     "1.49.1",
			wantVersion: "1.49.0",
			wantOK:      true,
		},
		{
			name: "playwright major minor mismatch does not match",
			browsers: map[string]Versions{
				"chromium": {
					Versions: map[string]*Browser{
						"1.48.9": {Protocol: "playwright"},
					},
				},
			},
			browserName: "chromium",
			version:     "1.49.1",
			wantVersion: "1.49.1",
			wantOK:      false,
		},
		{
			name: "playwright ambiguous compatible candidates fail",
			browsers: map[string]Versions{
				"chromium": {
					Versions: map[string]*Browser{
						"1.49.0": {Protocol: "playwright"},
						"1.49.2": {Protocol: "playwright"},
					},
				},
			},
			browserName: "chromium",
			version:     "1.49.1",
			wantVersion: "1.49.1",
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{Browsers: tt.browsers}

			gotBrowser, gotVersion, gotOK := cfg.Find(tt.browserName, tt.version)

			require.Equal(t, tt.wantVersion, gotVersion)
			require.Equal(t, tt.wantOK, gotOK)
			if tt.wantOK {
				require.NotNil(t, gotBrowser)
				return
			}
			require.Nil(t, gotBrowser)
		})
	}
}
