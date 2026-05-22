package pgstore

import "testing"

func TestOperationFromSQL(t *testing.T) {
	cases := map[string]string{
		"":                              "other",
		"SELECT 1":                      "ping",
		"SELECT pg_advisory_lock($1)":   "advisory_lock",
		"SELECT pg_advisory_unlock($1)": "advisory_unlock",
		"CREATE TABLE IF NOT EXISTS profiles (email text)":                                 "migration",
		"CREATE INDEX IF NOT EXISTS profiles_email ON profiles (email)":                    "migration",
		"ALTER TABLE profiles ADD COLUMN x text":                                           "migration",
		"DROP TABLE profiles":                                                              "migration",
		"SELECT payload FROM session_events WHERE tank_session_id = $1":                    "select_session_events",
		"WITH x AS (SELECT 1) SELECT * FROM session_events":                                "select_session_events",
		"INSERT INTO session_events (tank_session_id, order_key) VALUES ($1, $2)":          "insert_session_events",
		"UPDATE profiles SET github_login = $1 WHERE email = $2":                           "update_profiles",
		"DELETE FROM sessions WHERE email = $1":                                            "delete_sessions",
		"SELECT email FROM profiles":                                                       "select_profiles",
		"INSERT INTO github_install_states (state, email, expires_at) VALUES ($1, $2, $3)": "insert_github_install_states",
		"DELETE FROM stream_auth_tickets WHERE expires_at < now() - interval '1 hour'":     "delete_stream_auth_tickets",
		"SELECT ticket FROM stream_auth_tickets WHERE ticket = $1 FOR UPDATE":              "select_stream_auth_tickets",
		"UPDATE stream_auth_tickets SET last_used_at = now() WHERE ticket = $1":            "update_stream_auth_tickets",
		"INSERT INTO sessions (email, session_scope, session_id) VALUES ($1, $2, $3)":      "insert_sessions",
		"UPDATE conversation_read_state SET last_read_order_key = $1":                      "update_conversation_read_state",
		"SELECT next_session_number FROM session_counters WHERE session_scope = $1":        "select_session_counters",
		"SELECT 1 FROM unknown_table WHERE id = $1":                                        "other",
		// Subquery shapes ("SELECT ... FROM (SELECT ...) sub") return
		// "other" by design — the extractor refuses to parse nested SQL
		// to keep cardinality predictable. The "operation=other"
		// PrometheusRule surfaces unmapped SQL the orchestrator started
		// running.
		"SELECT count(*) FROM (SELECT * FROM session_events) sub": "other",
	}
	for sql, want := range cases {
		if got := operationFromSQL(sql); got != want {
			t.Errorf("operationFromSQL(%q) = %q, want %q", sql, got, want)
		}
	}
}
