package profiles

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// UpdateInstallation upserts the GitHub installation_id (and optionally github_login)
// on the profile row for the given email.
func (s *CosmosStore) UpdateInstallation(ctx context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}

	// Read existing (or start from blank).
	existing, err := s.GetOrCreate(ctx, normalized)
	if err != nil {
		return Profile{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	doc := map[string]any{
		"id":              normalized,
		"email":           normalized,
		"installation_id": installationID,
		"updated_at":      now,
	}
	if githubLogin != nil {
		doc["github_login"] = *githubLogin
	} else if existing.GitHubLogin != nil {
		doc["github_login"] = *existing.GitHubLogin
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return Profile{}, err
	}

	pk := azcosmos.NewPartitionKeyString(normalized)
	_, err = s.container.UpsertItem(ctx, pk, raw, nil)
	if err != nil {
		return Profile{}, err
	}

	profile := Profile{
		Email:          normalized,
		GitHubLogin:    githubLogin,
		InstallationID: &installationID,
	}
	if profile.GitHubLogin == nil {
		profile.GitHubLogin = existing.GitHubLogin
	}
	return profile, nil
}

// UpdateInstallation is a no-op for StubStore.
func (StubStore) UpdateInstallation(_ context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	return Profile{
		Email:          strings.ToLower(strings.TrimSpace(email)),
		GitHubLogin:    githubLogin,
		InstallationID: &installationID,
	}, nil
}
