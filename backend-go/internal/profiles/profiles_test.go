package profiles

import "testing"

func TestProfileFromMapPreservesRunPrefs(t *testing.T) {
	doc := map[string]any{
		"email":           "u@x",
		"installation_id": float64(7),
		"github_login":    "ghuser",
		"run_prefs":       map[string]any{"showThinking": false},
		"pinned_repos":    []any{"nelsong6/tank-operator", "nelsong6/glimmung"},
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
	if len(p.PinnedRepos) != 2 || p.PinnedRepos[0] != "nelsong6/tank-operator" || p.PinnedRepos[1] != "nelsong6/glimmung" {
		t.Fatalf("pinned_repos = %#v", p.PinnedRepos)
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
	if p.PinnedRepos == nil || len(p.PinnedRepos) != 0 {
		t.Fatalf("pinned_repos = %#v, want empty slice", p.PinnedRepos)
	}
}
