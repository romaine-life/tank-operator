package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Profile struct {
	Email          string  `json:"email"`
	GitHubLogin    *string `json:"github_login"`
	InstallationID *int64  `json:"installation_id"`
	// RunPrefs is opaque on the wire — the SPA owns the schema (chat font
	// scale, sound volume, etc., see frontend/src/App.tsx → RunPrefs).
	// Storing as a free-form map lets us evolve UI prefs without coupling
	// to a schema migration. Nil when the user has never set prefs.
	RunPrefs map[string]any `json:"run_prefs,omitempty"`
}

type Store interface {
	GetOrCreate(ctx context.Context, email string) (Profile, error)
}

// PostgresStore reads/writes the `profiles` table on Azure Database for
// PostgreSQL. The orchestrator pod connects via its workload-identity-issued
// AAD token; see internal/pgstore for the pool setup.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) GetOrCreate(ctx context.Context, email string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	const q = `
		SELECT email, github_login, installation_id, run_prefs
		FROM profiles
		WHERE email = $1
	`
	var (
		gotEmail string
		login    *string
		instID   *int64
		prefsRaw []byte
	)
	err := s.pool.QueryRow(ctx, q, normalized).Scan(&gotEmail, &login, &instID, &prefsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{Email: normalized}, nil
	}
	if err != nil {
		return Profile{}, err
	}
	p := Profile{
		Email:          gotEmail,
		GitHubLogin:    login,
		InstallationID: instID,
	}
	if len(prefsRaw) > 0 {
		var prefs map[string]any
		if err := json.Unmarshal(prefsRaw, &prefs); err != nil {
			return Profile{}, err
		}
		p.RunPrefs = prefs
	}
	if p.Email == "" {
		p.Email = normalized
	}
	return p, nil
}

type StubStore struct{}

func (StubStore) GetOrCreate(_ context.Context, email string) (Profile, error) {
	return Profile{Email: strings.ToLower(strings.TrimSpace(email))}, nil
}

// profileFromMap rehydrates a Profile from an untyped map. Used by the
// update path when an upsert returns the merged row to the caller.
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
