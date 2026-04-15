package session

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type CapsPolicy string

const (
	PolicyStrict      CapsPolicy = "strict"
	PolicyPermissive  CapsPolicy = "permissive"
	hostnameMaxLen               = 63
	labelMaxLen                  = 128
	fileNameMaxLen               = 128
)

var (
	hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)
	fileNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
	labelRe    = regexp.MustCompile(`^[\x20-\x7E]{0,128}$`)
)

// Sanitize validates and normalizes user-supplied capabilities according
// to policy. Strict mode is the default for production: capabilities that
// would let a tenant influence the host (custom DNS, extra Docker
// networks, arbitrary process env vars) are rejected unless the caller is
// an admin. String fields that flow into Docker labels, hostnames or
// filenames are checked against conservative regexes to keep CRLF and
// path traversal out of downstream subsystems regardless of policy.
//
// Permissive mode preserves the legacy upstream-Selenoid behavior and
// only enforces the structural string checks; it should be used only
// when the operator trusts every caller to supply benign input.
func Sanitize(caps *Caps, policy CapsPolicy, isAdmin bool) error {
	if caps == nil {
		return nil
	}

	if policy == "" {
		policy = PolicyStrict
	}

	if policy == PolicyStrict && !isAdmin {
		if len(caps.Env) > 0 {
			return fmt.Errorf("caps.env not allowed under -caps-policy=strict")
		}
		if len(caps.DNSServers) > 0 {
			return fmt.Errorf("caps.dnsServers not allowed under -caps-policy=strict")
		}
		if len(caps.HostsEntries) > 0 {
			return fmt.Errorf("caps.hostsEntries not allowed under -caps-policy=strict")
		}
		if len(caps.AdditionalNetworks) > 0 {
			return fmt.Errorf("caps.additionalNetworks not allowed under -caps-policy=strict")
		}
		if len(caps.ApplicationContainers) > 0 {
			return fmt.Errorf("caps.applicationContainers not allowed under -caps-policy=strict")
		}
	}

	if caps.ContainerHostname != "" && !hostnameRe.MatchString(caps.ContainerHostname) {
		return fmt.Errorf("caps.containerHostname %q must match %s", caps.ContainerHostname, hostnameRe.String())
	}

	if caps.TestName != "" {
		if len(caps.TestName) > labelMaxLen {
			caps.TestName = caps.TestName[:labelMaxLen]
		}
		if !labelRe.MatchString(caps.TestName) {
			return fmt.Errorf("caps.name contains forbidden characters (control chars or non-ASCII)")
		}
	}

	for k, v := range caps.Labels {
		if !labelRe.MatchString(k) {
			return fmt.Errorf("caps.labels key %q contains forbidden characters", k)
		}
		if len(v) > labelMaxLen {
			caps.Labels[k] = v[:labelMaxLen]
			v = caps.Labels[k]
		}
		if !labelRe.MatchString(v) {
			return fmt.Errorf("caps.labels[%q] contains forbidden characters", k)
		}
	}

	if caps.VideoName != "" {
		base := filepath.Base(caps.VideoName)
		if base != caps.VideoName || !fileNameRe.MatchString(base) {
			return fmt.Errorf("caps.videoName %q must be a basename matching %s", caps.VideoName, fileNameRe.String())
		}
	}
	if caps.LogName != "" {
		base := filepath.Base(caps.LogName)
		if base != caps.LogName || !fileNameRe.MatchString(base) {
			return fmt.Errorf("caps.logName %q must be a basename matching %s", caps.LogName, fileNameRe.String())
		}
	}

	if strings.ContainsAny(caps.S3KeyPattern, "\r\n") {
		return fmt.Errorf("caps.s3KeyPattern contains control characters")
	}
	return nil
}
