package profiles

import "testing"

func TestProfileFromMapPreservesRunPrefs(t *testing.T) {
	doc := map[string]any{
		"email":           "u@x",
		"installation_id": float64(7),
		"github_login":    "ghuser",
		"run_prefs":       map[string]any{"showThinking": false},
	}
	p := profileFromMap(doc)
	if p.Email != "u@x" {
		t.Fatalf("email = %q", p.Email)
	}
	if p.InstallationID == nil || *p.InstallationID != 7 {
		t.Fatalf("installation_id = %#v", p.InstallationID)
	}
	if p.GitHubLogin == nil || *p.GitHubLogin != "ghuser" {
		t.Fatalf("github_login = %#v", p.GitHubLogin)
	}
	if p.RunPrefs == nil || p.RunPrefs["showThinking"] != false {
		t.Fatalf("run_prefs = %#v", p.RunPrefs)
	}
}

func TestProfileFromMapHandlesNullables(t *testing.T) {
	doc := map[string]any{
		"email": "u@x",
	}
	p := profileFromMap(doc)
	if p.Email != "u@x" {
		t.Fatalf("email = %q", p.Email)
	}
	if p.GitHubLogin != nil {
		t.Fatalf("github_login = %#v", p.GitHubLogin)
	}
	if p.InstallationID != nil {
		t.Fatalf("installation_id = %#v", p.InstallationID)
	}
}
