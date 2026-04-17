package protect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeGroupsFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestFileGroupsProvider_Load(t *testing.T) {
	path := writeGroupsFile(t, `{"qa-payments": ["alice", "jenkins-bot"], "qa-growth": ["bob", "alice"]}`)
	p, err := NewFileGroupsProvider(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.GroupsFor("alice"); !reflect.DeepEqual(got, []string{"qa-growth", "qa-payments"}) {
		t.Fatalf("alice groups: %v", got)
	}
	if got := p.GroupsFor("jenkins-bot"); !reflect.DeepEqual(got, []string{"qa-payments"}) {
		t.Fatalf("jenkins-bot groups: %v", got)
	}
	if got := p.GroupsFor("bob"); !reflect.DeepEqual(got, []string{"qa-growth"}) {
		t.Fatalf("bob groups: %v", got)
	}
	if got := p.GroupsFor("mallory"); got != nil {
		t.Fatalf("unknown user must return nil, got %v", got)
	}
	if got := p.GroupsFor(""); got != nil {
		t.Fatalf("empty user must return nil, got %v", got)
	}
}

func TestFileGroupsProvider_GroupsForReturnsCopy(t *testing.T) {
	path := writeGroupsFile(t, `{"qa": ["alice"]}`)
	p, err := NewFileGroupsProvider(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := p.GroupsFor("alice")
	got[0] = "tampered"
	again := p.GroupsFor("alice")
	if again[0] != "qa" {
		t.Fatalf("caller must not mutate internal state; got %v", again)
	}
}

func TestFileGroupsProvider_Reload(t *testing.T) {
	path := writeGroupsFile(t, `{"qa": ["alice"]}`)
	p, err := NewFileGroupsProvider(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"ops": ["alice", "bob"]}`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := p.GroupsFor("alice"); !reflect.DeepEqual(got, []string{"ops"}) {
		t.Fatalf("after reload alice: %v", got)
	}
	if got := p.GroupsFor("bob"); !reflect.DeepEqual(got, []string{"ops"}) {
		t.Fatalf("after reload bob: %v", got)
	}
}

func TestFileGroupsProvider_ReloadKeepsOldOnError(t *testing.T) {
	path := writeGroupsFile(t, `{"qa": ["alice"]}`)
	p, err := NewFileGroupsProvider(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := p.Reload(); err == nil {
		t.Fatal("expected parse error")
	}
	if got := p.GroupsFor("alice"); !reflect.DeepEqual(got, []string{"qa"}) {
		t.Fatalf("state must be preserved on reload failure; got %v", got)
	}
}

func TestParseGroupsFile_Errors(t *testing.T) {
	cases := map[string]string{
		"empty file":       ``,
		"null":             `null`,
		"empty group":      `{"": ["alice"]}`,
		"empty member":     `{"qa": [""]}`,
		"dup member":       `{"qa": ["alice", "alice"]}`,
		"control in group": "{\"q\ta\": [\"alice\"]}",
		"control in user":  "{\"qa\": [\"ali\nce\"]}",
		"non-string":       `{"qa": [123]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGroupsFile([]byte(body)); err == nil {
				t.Fatalf("%s: expected error", name)
			}
		})
	}
}

func TestStaticGroups_ReturnsNil(t *testing.T) {
	var p GroupsProvider = StaticGroups{}
	if got := p.GroupsFor("alice"); got != nil {
		t.Fatalf("StaticGroups must always return nil, got %v", got)
	}
}
