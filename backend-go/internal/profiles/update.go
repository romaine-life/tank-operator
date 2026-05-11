package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// readRawDoc fetches the existing profile doc as an untyped map so a
// caller can splice fields into it without clobbering unrelated keys.
// Returns an empty map when the doc doesn't exist yet — the caller is
// responsible for seeding `id` and `email`.
//
// We have to go through a raw map because the Profile struct only types
// the fields the orchestrator knows about today. The doc may carry
// future fields (or fields a newer SPA wrote that this build doesn't
// know about); a read-decode-write round-trip on the typed struct would
// drop them. The merge pattern here is the same one Discord/Slack-style
// settings APIs use: never overwrite the whole doc, splice the delta.
func (s *CosmosStore) readRawDoc(ctx context.Context, email string) (map[string]any, error) {
	pk := azcosmos.NewPartitionKeyString(email)
	response, err := s.container.ReadItem(ctx, pk, email, nil)
	if err != nil {
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound {
			return map[string]any{}, nil
		}
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(response.Value, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateInstallation upserts the GitHub installation_id (and optionally github_login)
// on the profile row for the given email. Other fields on the doc (e.g. run_prefs)
// are preserved via the readRawDoc merge.
func (s *CosmosStore) UpdateInstallation(ctx context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}

	doc, err := s.readRawDoc(ctx, normalized)
	if err != nil {
		return Profile{}, err
	}
	doc["id"] = normalized
	doc["email"] = normalized
	doc["installation_id"] = installationID
	doc["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if githubLogin != nil {
		doc["github_login"] = *githubLogin
	}

	if err := s.upsertDoc(ctx, normalized, doc); err != nil {
		return Profile{}, err
	}
	return profileFromMap(doc), nil
}

// UpdatePrefs upserts the SPA's run-pane preferences. The body is opaque
// on the orchestrator side — see RunPrefs in the SPA for the shape. Other
// fields on the doc are preserved.
func (s *CosmosStore) UpdatePrefs(ctx context.Context, email string, prefs map[string]any) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	if prefs == nil {
		prefs = map[string]any{}
	}

	doc, err := s.readRawDoc(ctx, normalized)
	if err != nil {
		return Profile{}, err
	}
	doc["id"] = normalized
	doc["email"] = normalized
	doc["run_prefs"] = prefs
	doc["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := s.upsertDoc(ctx, normalized, doc); err != nil {
		return Profile{}, err
	}
	return profileFromMap(doc), nil
}

func (s *CosmosStore) upsertDoc(ctx context.Context, partitionEmail string, doc map[string]any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(partitionEmail), raw, nil)
	return err
}

func profileFromMap(doc map[string]any) Profile {
	p := Profile{}
	if v, ok := doc["email"].(string); ok {
		p.Email = v
	}
	if v, ok := doc["github_login"].(string); ok {
		login := v
		p.GitHubLogin = &login
	}
	switch v := doc["installation_id"].(type) {
	case float64:
		id := int64(v)
		p.InstallationID = &id
	case int64:
		id := v
		p.InstallationID = &id
	case int:
		id := int64(v)
		p.InstallationID = &id
	}
	if v, ok := doc["run_prefs"].(map[string]any); ok {
		p.RunPrefs = v
	}
	return p
}

// UpdateInstallation is a no-op for StubStore.
func (StubStore) UpdateInstallation(_ context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	return Profile{
		Email:          strings.ToLower(strings.TrimSpace(email)),
		GitHubLogin:    githubLogin,
		InstallationID: &installationID,
	}, nil
}

// UpdatePrefs is a no-op for StubStore — the SPA falls back to
// localStorage when the orchestrator runs without Cosmos configured.
func (StubStore) UpdatePrefs(_ context.Context, email string, prefs map[string]any) (Profile, error) {
	return Profile{
		Email:    strings.ToLower(strings.TrimSpace(email)),
		RunPrefs: prefs,
	}, nil
}
