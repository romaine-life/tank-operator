package profiles

import (
	"context"
	"encoding/json"
	"strings"
)

// UpdateInstallation upserts the GitHub installation_id (and optionally
// github_login) on the profile row for the given email. Other columns on the
// row are preserved by listing only the touched columns in the UPDATE clause.
func (s *PostgresStore) UpdateInstallation(ctx context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	const q = `
		INSERT INTO profiles (email, github_login, installation_id, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (email) DO UPDATE
		SET installation_id = EXCLUDED.installation_id,
			github_login    = COALESCE(EXCLUDED.github_login, profiles.github_login),
			updated_at      = now()
		RETURNING email, github_login, installation_id, run_prefs, COALESCE(pinned_repos, '{}'::text[]),
		          to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
	`
	var (
		gotEmail string
		login    *string
		instID   *int64
		prefsRaw []byte
		pins     []string
		updated  string
	)
	if err := s.pool.QueryRow(ctx, q, normalized, githubLogin, installationID).
		Scan(&gotEmail, &login, &instID, &prefsRaw, &pins, &updated); err != nil {
		return Profile{}, err
	}
	p := Profile{
		Email:          gotEmail,
		GitHubLogin:    login,
		InstallationID: instID,
		UpdatedAt:      updated,
		PinnedRepos:    pins,
	}
	if len(prefsRaw) > 0 {
		var prefs map[string]any
		if err := json.Unmarshal(prefsRaw, &prefs); err != nil {
			return Profile{}, err
		}
		p.RunPrefs = prefs
	}
	return p, nil
}

// UpdatePrefs upserts the SPA's run-pane preferences. The body is opaque on
// the orchestrator side — see RunPrefs in the SPA for the shape.
func (s *PostgresStore) UpdatePrefs(ctx context.Context, email string, prefs map[string]any) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	if prefs == nil {
		prefs = map[string]any{}
	}
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		return Profile{}, err
	}
	const q = `
		INSERT INTO profiles (email, run_prefs, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (email) DO UPDATE
		SET run_prefs  = EXCLUDED.run_prefs,
			updated_at = now()
		RETURNING email, github_login, installation_id, run_prefs, COALESCE(pinned_repos, '{}'::text[]),
		          to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
	`
	var (
		gotEmail   string
		login      *string
		instID     *int64
		gotPrefsJS []byte
		pins       []string
		updated    string
	)
	if err := s.pool.QueryRow(ctx, q, normalized, prefsJSON).
		Scan(&gotEmail, &login, &instID, &gotPrefsJS, &pins, &updated); err != nil {
		return Profile{}, err
	}
	p := Profile{
		Email:          gotEmail,
		GitHubLogin:    login,
		InstallationID: instID,
		UpdatedAt:      updated,
		PinnedRepos:    pins,
	}
	if len(gotPrefsJS) > 0 {
		var gotPrefs map[string]any
		if err := json.Unmarshal(gotPrefsJS, &gotPrefs); err != nil {
			return Profile{}, err
		}
		p.RunPrefs = gotPrefs
	}
	return p, nil
}

// UpdatePinnedRepos replaces the caller's durable splash-picker pins.
func (s *PostgresStore) UpdatePinnedRepos(ctx context.Context, email string, repos []string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	if repos == nil {
		repos = []string{}
	}
	const q = `
		INSERT INTO profiles (email, pinned_repos, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (email) DO UPDATE
		SET pinned_repos = EXCLUDED.pinned_repos,
			updated_at   = now()
		RETURNING email, github_login, installation_id, run_prefs, COALESCE(pinned_repos, '{}'::text[]),
		          to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
	`
	var (
		gotEmail   string
		login      *string
		instID     *int64
		gotPrefsJS []byte
		pins       []string
		updated    string
	)
	if err := s.pool.QueryRow(ctx, q, normalized, repos).
		Scan(&gotEmail, &login, &instID, &gotPrefsJS, &pins, &updated); err != nil {
		return Profile{}, err
	}
	p := Profile{
		Email:          gotEmail,
		GitHubLogin:    login,
		InstallationID: instID,
		UpdatedAt:      updated,
		PinnedRepos:    pins,
	}
	if len(gotPrefsJS) > 0 {
		var gotPrefs map[string]any
		if err := json.Unmarshal(gotPrefsJS, &gotPrefs); err != nil {
			return Profile{}, err
		}
		p.RunPrefs = gotPrefs
	}
	return p, nil
}

// UpdateInstallation is a no-op for StubStore.
func (StubStore) UpdateInstallation(_ context.Context, email string, installationID int64, githubLogin *string) (Profile, error) {
	return Profile{
		Email:          strings.ToLower(strings.TrimSpace(email)),
		GitHubLogin:    githubLogin,
		InstallationID: &installationID,
		PinnedRepos:    []string{},
	}, nil
}

// UpdatePrefs is a no-op for StubStore — the SPA falls back to localStorage
// when the orchestrator runs without Postgres configured.
func (StubStore) UpdatePrefs(_ context.Context, email string, prefs map[string]any) (Profile, error) {
	return Profile{
		Email:       strings.ToLower(strings.TrimSpace(email)),
		RunPrefs:    prefs,
		PinnedRepos: []string{},
	}, nil
}

// UpdatePinnedRepos echoes the request for StubStore. Production persists this
// on the Postgres profiles row; the stub exists only for no-Postgres local
// startup.
func (StubStore) UpdatePinnedRepos(_ context.Context, email string, repos []string) (Profile, error) {
	if repos == nil {
		repos = []string{}
	}
	return Profile{
		Email:       strings.ToLower(strings.TrimSpace(email)),
		PinnedRepos: repos,
	}, nil
}
