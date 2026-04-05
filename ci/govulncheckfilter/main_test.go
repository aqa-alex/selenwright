package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunFailsOnReachableFixableVulnerability(t *testing.T) {
	input := strings.NewReader(`{"osv":{"id":"GO-1","summary":"reachable and fixable"}}
{"finding":{"osv":"GO-1","fixed_version":"v1.2.3","trace":[{"module":"stdlib","package":"net/http","function":"Serve","position":{"filename":"main.go","line":10,"column":2}}]}}
`)

	var out bytes.Buffer
	exitCode := run(input, &out)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(out.String(), "GO-1") {
		t.Fatalf("expected output to mention vulnerability id, got %q", out.String())
	}
	if !strings.Contains(out.String(), "fixed=v1.2.3") {
		t.Fatalf("expected output to mention fixed version, got %q", out.String())
	}
}

func TestRunIgnoresReachableNoFixVulnerability(t *testing.T) {
	input := strings.NewReader(`{"osv":{"id":"GO-2","summary":"reachable but no fix"}}
{"finding":{"osv":"GO-2","trace":[{"module":"github.com/docker/docker","package":"github.com/docker/docker/client","function":"NewClientWithOpts","position":{"filename":"main.go","line":12,"column":4}}]}}
`)

	var out bytes.Buffer
	exitCode := run(input, &out)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(out.String(), "left as warnings") {
		t.Fatalf("expected warning output, got %q", out.String())
	}
}

func TestRunIgnoresImportOnlyFixableVulnerability(t *testing.T) {
	input := strings.NewReader(`{"osv":{"id":"GO-3","summary":"import only"}}
{"finding":{"osv":"GO-3","fixed_version":"v9.9.9","trace":[{"module":"example.com/mod","package":"example.com/mod/pkg"}]}}
`)

	var out bytes.Buffer
	exitCode := run(input, &out)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(out.String(), "import-only or module-only findings") {
		t.Fatalf("expected import-only summary, got %q", out.String())
	}
}
