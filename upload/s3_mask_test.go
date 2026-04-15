//go:build s3
// +build s3

package upload

import (
	"strings"
	"testing"
)

// TestMaskAccessKey covers the log-redaction helper used by S3Uploader.Init.
// The full key must never round-trip to the masked output; short inputs
// collapse to a fixed redaction placeholder with no partial leak.
func TestMaskAccessKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "***"},
		{"short", "AKI", "***"},
		{"boundary", "AKIA", "***"},
		{"typical", "AKIAIOSFODNN7EXAMPLE", "AKIA***"},
		{"lower", "akiaexample123", "akia***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskAccessKey(tc.in)
			if got != tc.want {
				t.Fatalf("maskAccessKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(tc.in) > 4 && strings.Contains(got, tc.in[4:]) {
				t.Fatalf("masked output %q must not contain tail of original %q", got, tc.in)
			}
		})
	}
}
