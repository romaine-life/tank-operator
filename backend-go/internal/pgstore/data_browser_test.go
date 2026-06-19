package pgstore

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestValidDataTableIdent(t *testing.T) {
	valid := []string{"sessions", "session_events", "_x", "a1", "profiles", "schema_migrations"}
	for _, name := range valid {
		if !ValidDataTableIdent(name) {
			t.Errorf("ValidDataTableIdent(%q) = false, want true", name)
		}
	}
	invalid := []string{
		"", "Sessions", "1abc", "a-b", "a b", "a;b", "a.b",
		"drop table", "sessions--", "\"sessions\"", "session'", strings.Repeat("a", 64),
	}
	for _, name := range invalid {
		if ValidDataTableIdent(name) {
			t.Errorf("ValidDataTableIdent(%q) = true, want false", name)
		}
	}
}

func TestIsSecretColumn(t *testing.T) {
	secret := []struct{ table, col string }{
		{"github_install_states", "state"},
		{"stream_auth_tickets", "ticket"},
		{"message_link_shares", "token_hash"},
		{"anything", "access_token"},
		{"anything", "Refresh_Token"}, // case-insensitive
		{"anything", "client_secret"},
		{"anything", "user_password"},
		{"anything", "api_key"},
		{"anything", "github_credential"},
	}
	for _, c := range secret {
		if !isSecretColumn(c.table, c.col) {
			t.Errorf("isSecretColumn(%q,%q) = false, want true (secret)", c.table, c.col)
		}
	}
	// `state` is only secret in the install-nonce table; the common session
	// *_state columns must stay visible, and ordinary identity columns too.
	notSecret := []struct{ table, col string }{
		{"sessions", "test_state"},
		{"sessions", "rollout_state"},
		{"sessions", "clone_state"},
		{"sessions", "email"},
		{"profiles", "github_login"},
		{"orchestrations", "state"},
		{"avatar_assets", "avatar_blob_key"},
	}
	for _, c := range notSecret {
		if isSecretColumn(c.table, c.col) {
			t.Errorf("isSecretColumn(%q,%q) = true, want false (visible)", c.table, c.col)
		}
	}
}

func TestKeysetSafeTypeAndPlan(t *testing.T) {
	safe := []string{"text", "bigint", "integer", "character varying(255)", "timestamp with time zone", "boolean", "date"}
	for _, ft := range safe {
		if !keysetSafeType(ft) {
			t.Errorf("keysetSafeType(%q) = false, want true", ft)
		}
	}
	unsafe := []string{"jsonb", "bytea", "text[]", "uuid", "numeric", "double precision"}
	for _, ft := range unsafe {
		if keysetSafeType(ft) {
			t.Errorf("keysetSafeType(%q) = true, want false", ft)
		}
	}

	cols := []columnMeta{
		{Name: "email", Type: "text"},
		{Name: "n", Type: "bigint"},
		{Name: "blob", Type: "bytea", IsBytea: true},
	}
	if p := planRows("sessions", cols, []string{"email", "n"}); len(p.pk) != 2 || p.pkTypes[1] != "bigint" {
		t.Fatalf("planRows safe PK = %+v, want keyset over email,n", p)
	}
	if p := planRows("sessions", cols, nil); len(p.pk) != 0 {
		t.Errorf("planRows(no pk) = %+v, want empty plan", p)
	}
	if p := planRows("sessions", cols, []string{"blob"}); len(p.pk) != 0 {
		t.Errorf("planRows(bytea pk) = %+v, want empty plan (degrade to single page)", p)
	}
	// A secret PK column must not become a keyset cursor — that would carry the
	// redacted value out in next_cursor. Such tables degrade to a single page.
	secretPKCols := []columnMeta{{Name: "ticket", Type: "text"}, {Name: "email", Type: "text"}}
	if p := planRows("stream_auth_tickets", secretPKCols, []string{"ticket"}); len(p.pk) != 0 {
		t.Errorf("planRows(secret pk) = %+v, want empty plan (no secret in cursor)", p)
	}
}

func TestBuildRowsQuery(t *testing.T) {
	cases := []struct {
		name      string
		table     string
		cols      []columnMeta
		pk        []string
		hasCursor bool
		limit     int
		want      string
	}{
		{
			name:  "single text pk no cursor",
			table: "profiles",
			cols:  []columnMeta{{Name: "email", Type: "text"}, {Name: "github_login", Type: "text"}},
			pk:    []string{"email"},
			limit: 100,
			want:  `SELECT "email", "github_login" FROM "public"."profiles" ORDER BY "email" LIMIT 101`,
		},
		{
			name:      "composite pk with cursor casts each key",
			table:     "sessions",
			cols:      []columnMeta{{Name: "email", Type: "text"}, {Name: "session_id", Type: "text"}},
			pk:        []string{"email", "session_id"},
			hasCursor: true,
			limit:     50,
			want:      `SELECT "email", "session_id" FROM "public"."sessions" WHERE ("email", "session_id") > (CAST($1 AS text), CAST($2 AS text)) ORDER BY "email", "session_id" LIMIT 51`,
		},
		{
			name:  "bytea column exposes size only",
			table: "avatar_assets",
			cols:  []columnMeta{{Name: "id", Type: "text"}, {Name: "avatar_bytes", Type: "bytea", IsBytea: true}},
			pk:    []string{"id"},
			limit: 10,
			want:  `SELECT "id", octet_length("avatar_bytes") AS "avatar_bytes" FROM "public"."avatar_assets" ORDER BY "id" LIMIT 11`,
		},
		{
			name:  "no pk falls back to ordinal order, single page",
			table: "schema_migrations",
			cols:  []columnMeta{{Name: "id", Type: "text"}, {Name: "checksum", Type: "text"}},
			pk:    nil,
			limit: 25,
			want:  `SELECT "id", "checksum" FROM "public"."schema_migrations" ORDER BY 1 LIMIT 26`,
		},
		{
			name:      "bigint pk casts to bigint",
			table:     "bug_labels",
			cols:      []columnMeta{{Name: "id", Type: "bigint"}, {Name: "name", Type: "text"}},
			pk:        []string{"id"},
			hasCursor: true,
			limit:     100,
			want:      `SELECT "id", "name" FROM "public"."bug_labels" WHERE ("id") > (CAST($1 AS bigint)) ORDER BY "id" LIMIT 101`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planRows(tc.table, tc.cols, tc.pk)
			got := buildRowsQuery(tc.table, tc.cols, plan, tc.hasCursor, tc.limit)
			if got != tc.want {
				t.Errorf("buildRowsQuery mismatch:\n got: %s\nwant: %s", got, tc.want)
			}
			// No caller value is ever interpolated; only $-placeholders and
			// quoted identifiers reach the query text.
			if strings.Contains(got, "';") || strings.Contains(got, "--") {
				t.Errorf("query contains raw injection markers: %s", got)
			}
		})
	}
}

func TestRowCursorRoundTrip(t *testing.T) {
	vals := []string{"owner@example.com", "203", "2026-06-19T00:00:00Z"}
	enc := encodeRowCursor(vals)
	if enc == "" {
		t.Fatal("encodeRowCursor returned empty")
	}
	got, err := decodeRowCursor(enc)
	if err != nil {
		t.Fatalf("decodeRowCursor error: %v", err)
	}
	if len(got) != len(vals) {
		t.Fatalf("round-trip len=%d want %d", len(got), len(vals))
	}
	for i := range vals {
		if got[i] != vals[i] {
			t.Errorf("round-trip[%d]=%q want %q", i, got[i], vals[i])
		}
	}

	if v, err := decodeRowCursor(""); err != nil || v != nil {
		t.Errorf("empty cursor should decode to (nil,nil); got (%v,%v)", v, err)
	}
	if _, err := decodeRowCursor("!!!not-base64!!!"); err == nil {
		t.Error("malformed cursor should return an error")
	}
	// A syntactically valid base64 payload that is not a JSON string array
	// must be rejected, proving decodeRowCursor refuses forged shapes.
	badShape := base64.RawURLEncoding.EncodeToString([]byte(`{"not":"an array"}`))
	if _, err := decodeRowCursor(badShape); err == nil {
		t.Error("non-array cursor payload should return an error")
	}
}

func TestApplyCellPolicy(t *testing.T) {
	// Secret column -> placeholder regardless of the underlying value.
	if got := applyCellPolicy("stream_auth_tickets", columnMeta{Name: "ticket"}, "super-secret-bearer"); got != dataRedactedPlaceholder {
		t.Errorf("secret cell = %v, want placeholder", got)
	}
	// bytea column already arrives as its octet length (an int); passed through.
	if got := applyCellPolicy("avatar_assets", columnMeta{Name: "avatar_bytes", Type: "bytea", IsBytea: true}, int64(4096)); got != int64(4096) {
		t.Errorf("bytea cell = %v, want 4096", got)
	}
	// Ordinary value passes through json-safe.
	if got := applyCellPolicy("profiles", columnMeta{Name: "email"}, "owner@example.com"); got != "owner@example.com" {
		t.Errorf("value cell = %v, want email", got)
	}
	// Timestamps normalize to RFC3339 UTC.
	ts := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if got := applyCellPolicy("sessions", columnMeta{Name: "created_at"}, ts); got != "2026-06-19T12:00:00Z" {
		t.Errorf("time cell = %v, want RFC3339", got)
	}
}

func TestColumnKind(t *testing.T) {
	if k := columnKind("stream_auth_tickets", columnMeta{Name: "ticket"}); k != "redacted" {
		t.Errorf("secret kind = %q, want redacted", k)
	}
	if k := columnKind("avatar_assets", columnMeta{Name: "avatar_bytes", Type: "bytea", IsBytea: true}); k != "bytes" {
		t.Errorf("bytea kind = %q, want bytes", k)
	}
	if k := columnKind("profiles", columnMeta{Name: "email", Type: "text"}); k != "value" {
		t.Errorf("plain kind = %q, want value", k)
	}
}

func TestTruncateCell(t *testing.T) {
	short := "hello"
	if got := truncateCell(short); got != short {
		t.Errorf("short cell changed: %q", got)
	}
	long := strings.Repeat("a", dataBrowserMaxCellChars+500)
	got := truncateCell(long)
	if len(got) >= len(long) {
		t.Errorf("long cell was not truncated (len=%d)", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncated cell missing marker: %q", got[len(got)-40:])
	}
}
