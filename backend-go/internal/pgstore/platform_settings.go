package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const platformSettingTestSlotSessionDefaults = "test_slot_session_defaults"

var (
	ErrPlatformSettingNotFound     = errors.New("platform setting not found")
	ErrPlatformSettingsUnavailable = errors.New("platform settings table unavailable")
)

type TestSlotSessionDefaults struct {
	Mode      string `json:"mode"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type PlatformSettingsStore struct {
	pool *pgxpool.Pool
}

func NewPlatformSettingsStore(pool *pgxpool.Pool) *PlatformSettingsStore {
	return &PlatformSettingsStore{pool: pool}
}

func (s *PlatformSettingsStore) GetTestSlotSessionDefaults(ctx context.Context) (TestSlotSessionDefaults, error) {
	row, err := s.get(ctx, platformSettingTestSlotSessionDefaults)
	if err != nil {
		return TestSlotSessionDefaults{}, err
	}
	var value TestSlotSessionDefaults
	if err := json.Unmarshal(row.value, &value); err != nil {
		return TestSlotSessionDefaults{}, err
	}
	value.UpdatedBy = row.updatedBy
	value.UpdatedAt = row.updatedAt
	return normalizeTestSlotSessionDefaults(value), nil
}

func (s *PlatformSettingsStore) UpsertTestSlotSessionDefaults(ctx context.Context, value TestSlotSessionDefaults, updatedBy string) (TestSlotSessionDefaults, error) {
	normalized := normalizeTestSlotSessionDefaults(value)
	raw, err := json.Marshal(map[string]string{
		"mode":   normalized.Mode,
		"model":  normalized.Model,
		"effort": normalized.Effort,
	})
	if err != nil {
		return TestSlotSessionDefaults{}, err
	}
	const q = `
		INSERT INTO platform_settings (key, value, updated_by, updated_at)
		VALUES ($1, $2::jsonb, $3, now())
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()
		RETURNING value, updated_by,
		          to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
	`
	var (
		storedRaw []byte
		by        string
		at        string
	)
	if err := s.pool.QueryRow(ctx, q, platformSettingTestSlotSessionDefaults, raw, strings.TrimSpace(updatedBy)).Scan(&storedRaw, &by, &at); err != nil {
		if isUndefinedTableError(err) {
			return TestSlotSessionDefaults{}, ErrPlatformSettingsUnavailable
		}
		return TestSlotSessionDefaults{}, err
	}
	var stored TestSlotSessionDefaults
	if err := json.Unmarshal(storedRaw, &stored); err != nil {
		return TestSlotSessionDefaults{}, err
	}
	stored = normalizeTestSlotSessionDefaults(stored)
	stored.UpdatedBy = by
	stored.UpdatedAt = at
	return stored, nil
}

type platformSettingRow struct {
	value     []byte
	updatedBy string
	updatedAt string
}

func (s *PlatformSettingsStore) get(ctx context.Context, key string) (platformSettingRow, error) {
	const q = `
		SELECT value, updated_by,
		       to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		FROM platform_settings
		WHERE key = $1
	`
	var row platformSettingRow
	err := s.pool.QueryRow(ctx, q, key).Scan(&row.value, &row.updatedBy, &row.updatedAt)
	if isUndefinedTableError(err) {
		return platformSettingRow{}, ErrPlatformSettingsUnavailable
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return platformSettingRow{}, ErrPlatformSettingNotFound
	}
	if err != nil {
		return platformSettingRow{}, err
	}
	return row, nil
}

func normalizeTestSlotSessionDefaults(value TestSlotSessionDefaults) TestSlotSessionDefaults {
	return TestSlotSessionDefaults{
		Mode:      strings.TrimSpace(value.Mode),
		Model:     strings.TrimSpace(value.Model),
		Effort:    strings.TrimSpace(value.Effort),
		UpdatedBy: strings.TrimSpace(value.UpdatedBy),
		UpdatedAt: strings.TrimSpace(value.UpdatedAt),
	}
}
