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
