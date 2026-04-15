package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitize_StrictRejectsDangerousCapsForNonAdmin(t *testing.T) {
	cases := map[string]Caps{
		"env":                   {Env: []string{"LD_PRELOAD=/tmp/x"}},
		"dnsServers":            {DNSServers: []string{"8.8.8.8"}},
		"hostsEntries":          {HostsEntries: []string{"evil.com:1.2.3.4"}},
		"additionalNetworks":    {AdditionalNetworks: []string{"some-net"}},
		"applicationContainers": {ApplicationContainers: []string{"some-app"}},
	}
	for name, caps := range cases {
		t.Run(name, func(t *testing.T) {
			err := Sanitize(&caps, PolicyStrict, false)
			require.Error(t, err)
			require.Contains(t, err.Error(), "not allowed")
		})
	}
}

func TestSanitize_StrictAllowsDangerousCapsForAdmin(t *testing.T) {
	caps := Caps{
		Env:                   []string{"FOO=bar"},
		DNSServers:            []string{"8.8.8.8"},
		HostsEntries:          []string{"x:1.1.1.1"},
		AdditionalNetworks:    []string{"some-net"},
		ApplicationContainers: []string{"some-app"},
	}
	require.NoError(t, Sanitize(&caps, PolicyStrict, true))
}

func TestSanitize_PermissiveAllowsDangerousCapsForAnyone(t *testing.T) {
	caps := Caps{Env: []string{"FOO=bar"}, DNSServers: []string{"8.8.8.8"}}
	require.NoError(t, Sanitize(&caps, PolicyPermissive, false))
}

func TestSanitize_HostnameValidation(t *testing.T) {
	good := []string{"chrome-1", "alpha.bravo", "x"}
	for _, h := range good {
		t.Run("good_"+h, func(t *testing.T) {
			c := Caps{ContainerHostname: h}
			err := Sanitize(&c, PolicyStrict, true)
			if h == "alpha.bravo" {
				require.Error(t, err) // dot not in allow regex
			} else {
				require.NoError(t, err)
			}
		})
	}
	bad := []string{"-leading-dash", strings.Repeat("a", 64), "with space", "evil\nname", "под"}
	for _, h := range bad {
		t.Run("bad_"+h, func(t *testing.T) {
			c := Caps{ContainerHostname: h}
			require.Error(t, Sanitize(&c, PolicyStrict, true))
		})
	}
}

func TestSanitize_TestNameTruncatedAndValidated(t *testing.T) {
	c := Caps{TestName: strings.Repeat("a", 200)}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))
	require.Len(t, c.TestName, labelMaxLen)

	c = Caps{TestName: "ok name 1"}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{TestName: "evil\nname"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{TestName: "evil\rinjection"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))
}

func TestSanitize_LabelsValidation(t *testing.T) {
	c := Caps{Labels: map[string]string{"key": "value"}}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{Labels: map[string]string{"key\nbad": "v"}}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{Labels: map[string]string{"k": "value\rinjection"}}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	long := strings.Repeat("v", 200)
	c = Caps{Labels: map[string]string{"k": long}}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))
	require.Len(t, c.Labels["k"], labelMaxLen)
}

func TestSanitize_VideoLogNameRejectsTraversal(t *testing.T) {
	c := Caps{VideoName: "../escape.mp4"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{VideoName: "/abs/path.mp4"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{VideoName: "ok-name_v1.mp4"}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{LogName: "../etc/passwd"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{LogName: "valid.log"}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))
}

func TestSanitize_S3KeyPatternRejectsCRLF(t *testing.T) {
	c := Caps{S3KeyPattern: "ok/$sessionId"}
	require.NoError(t, Sanitize(&c, PolicyStrict, false))

	c = Caps{S3KeyPattern: "evil\r\ninject"}
	require.Error(t, Sanitize(&c, PolicyStrict, false))
}

func TestSanitize_NilAndEmptyAreNoOps(t *testing.T) {
	require.NoError(t, Sanitize(nil, PolicyStrict, false))
	require.NoError(t, Sanitize(&Caps{}, PolicyStrict, false))
	require.NoError(t, Sanitize(&Caps{}, PolicyPermissive, false))
}

func TestSanitize_DefaultPolicyIsStrict(t *testing.T) {
	c := Caps{Env: []string{"X=1"}}
	require.Error(t, Sanitize(&c, "", false), "empty policy must default to strict")
}
