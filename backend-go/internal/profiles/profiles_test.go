package profiles

import "testing"

func TestProfileFromDoc(t *testing.T) {
	login := "octocat"
	id := int64(123)
	profile, err := profileFromDoc([]byte(`{"email":"user@example.com","github_login":"octocat","installation_id":123}`))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Email != "user@example.com" || *profile.GitHubLogin != login || *profile.InstallationID != id {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestProfileFromDocAllowsNullProfileFields(t *testing.T) {
	profile, err := profileFromDoc([]byte(`{"email":"user@example.com","github_login":null,"installation_id":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if profile.GitHubLogin != nil || profile.InstallationID != nil {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestProfileFromDocParsesRunPrefs(t *testing.T) {
	profile, err := profileFromDoc([]byte(`{"email":"u@x","run_prefs":{"chatFontScale":1.2,"showThinking":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if profile.RunPrefs == nil {
		t.Fatalf("expected run_prefs, got nil")
	}
	if v, ok := profile.RunPrefs["chatFontScale"].(float64); !ok || v != 1.2 {
		t.Fatalf("chatFontScale = %#v", profile.RunPrefs["chatFontScale"])
	}
	if v, ok := profile.RunPrefs["showThinking"].(bool); !ok || !v {
		t.Fatalf("showThinking = %#v", profile.RunPrefs["showThinking"])
	}
}

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
