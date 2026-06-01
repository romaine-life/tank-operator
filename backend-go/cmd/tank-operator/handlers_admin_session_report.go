package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

const (
	sessionReportDefaultDays  = 30
	sessionReportMaxDays      = 365
	sessionReportDefaultLimit = 750
	sessionReportMaxLimit     = 2000
	sessionReportUnassigned   = "Unassigned"
)

type sessionReportRow struct {
	Owner     string     `json:"owner"`
	SessionID string     `json:"session_id"`
	Name      string     `json:"name"`
	Mode      string     `json:"mode"`
	Repos     []string   `json:"repos"`
	Visible   bool       `json:"visible"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Usage     tokenUsage `json:"usage"`
}

type sessionReportRepo struct {
	Repo         string     `json:"repo"`
	SessionCount int        `json:"session_count"`
	TotalTokens  int64      `json:"total_tokens"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	LastTouched  *time.Time `json:"last_touched,omitempty"`
}

type sessionReportTotals struct {
	SessionCount int   `json:"session_count"`
	RepoCount    int   `json:"repo_count"`
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	UsageEvents  int   `json:"usage_events"`
}

type tokenUsage struct {
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	UsageEvents  int   `json:"usage_events"`
}

type sessionUsageAccumulator struct {
	usageEvents int
	byTurn      map[string]tokenUsage
}

func (s *appServer) handleAdminSessionReport(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.pgPool == nil {
		writeError(w, http.StatusServiceUnavailable, "postgres is not configured")
		return
	}

	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	days, ok := boundedPositiveIntQuery(w, r, "days", sessionReportDefaultDays, sessionReportMaxDays)
	if !ok {
		return
	}
	limit, ok := boundedPositiveIntQuery(w, r, "limit", sessionReportDefaultLimit, sessionReportMaxLimit)
	if !ok {
		return
	}

	sessions, err := fetchSessionReportRows(r.Context(), s, scope, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session report: "+err.Error())
		return
	}
	if err := attachSessionReportUsage(r.Context(), s, scope, sessions); err != nil {
		writeError(w, http.StatusInternalServerError, "session report usage: "+err.Error())
		return
	}

	repos, totals := summarizeSessionReport(sessions)
	writeJSON(w, http.StatusOK, map[string]any{
		"description": "Cheap draft report over recent sessions. Repo attribution uses create-time sessions.repos only.",
		"scope":       scope,
		"days":        days,
		"limit":       limit,
		"attribution": "A session's latest-per-turn usage is credited to every selected repo on that session; multi-repo sessions intentionally appear in multiple repo buckets.",
		"totals":      totals,
		"repos":       repos,
		"sessions":    sessions,
		"fetched_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func boundedPositiveIntQuery(w http.ResponseWriter, r *http.Request, name string, def, max int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		writeError(w, http.StatusBadRequest, name+" must be a positive integer")
		return 0, false
	}
	if parsed > max {
		return max, true
	}
	return parsed, true
}

func fetchSessionReportRows(ctx context.Context, s *appServer, scope string, days, limit int) ([]sessionReportRow, error) {
	const q = `
		SELECT email, session_id, COALESCE(name, ''), mode, COALESCE(repos, '{}'::text[]),
		       visible, created_at, updated_at
		FROM sessions
		WHERE session_scope = $1
		  AND created_at >= now() - ($2::int * INTERVAL '1 day')
		ORDER BY created_at DESC
		LIMIT $3
	`
	rows, err := s.pgPool.Query(ctx, q, scope, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]sessionReportRow, 0, limit)
	for rows.Next() {
		var row sessionReportRow
		if err := rows.Scan(&row.Owner, &row.SessionID, &row.Name, &row.Mode, &row.Repos, &row.Visible, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func attachSessionReportUsage(ctx context.Context, s *appServer, scope string, sessions []sessionReportRow) error {
	if len(sessions) == 0 {
		return nil
	}
	keys := make([]string, 0, len(sessions))
	indexByKey := make(map[string]int, len(sessions))
	for i := range sessions {
		key := sessionmodel.SessionStorageKey(scope, sessions[i].SessionID)
		keys = append(keys, key)
		indexByKey[key] = i
	}

	const q = `
		SELECT tank_session_id, order_key, event_id, COALESCE(turn_id, ''), payload
		FROM session_events
		WHERE tank_session_id = ANY($1)
		  AND event_type = 'turn.usage'
		ORDER BY tank_session_id, order_key ASC
	`
	rows, err := s.pgPool.Query(ctx, q, keys)
	if err != nil {
		return err
	}
	defer rows.Close()

	accs := make(map[string]*sessionUsageAccumulator, len(sessions))
	for rows.Next() {
		var storageKey, orderKey, eventID, turnID string
		var payload []byte
		if err := rows.Scan(&storageKey, &orderKey, &eventID, &turnID, &payload); err != nil {
			return err
		}
		if _, ok := indexByKey[storageKey]; !ok {
			continue
		}
		acc := accs[storageKey]
		if acc == nil {
			acc = &sessionUsageAccumulator{byTurn: map[string]tokenUsage{}}
			accs[storageKey] = acc
		}
		acc.usageEvents++
		usage := tokenUsageFromEventPayload(payload)
		turnKey := strings.TrimSpace(turnID)
		if turnKey == "" {
			turnKey = strings.TrimSpace(eventID)
		}
		if turnKey == "" {
			turnKey = strings.TrimSpace(orderKey)
		}
		acc.byTurn[turnKey] = usage
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for storageKey, acc := range accs {
		i := indexByKey[storageKey]
		var usage tokenUsage
		usage.UsageEvents = acc.usageEvents
		for _, turnUsage := range acc.byTurn {
			usage.TotalTokens += turnUsage.TotalTokens
			usage.InputTokens += turnUsage.InputTokens
			usage.OutputTokens += turnUsage.OutputTokens
		}
		sessions[i].Usage = usage
	}
	return nil
}

func tokenUsageFromEventPayload(payload []byte) tokenUsage {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return tokenUsage{}
	}
	eventPayload, _ := doc["payload"].(map[string]any)
	usage, _ := eventPayload["usage"].(map[string]any)
	if len(usage) == 0 {
		return tokenUsage{}
	}

	input := numericUsageField(usage, "input_tokens", "prompt_tokens")
	output := numericUsageField(usage, "output_tokens", "completion_tokens") +
		numericUsageField(usage, "reasoning_output_tokens")
	total := numericUsageField(usage, "total_tokens")
	if total == 0 {
		total = input + output +
			numericUsageField(usage, "cache_creation_input_tokens") +
			numericUsageField(usage, "cache_read_input_tokens") +
			numericUsageField(usage, "cached_input_tokens")
	}
	return tokenUsage{
		TotalTokens:  total,
		InputTokens:  input,
		OutputTokens: output,
	}
}

func numericUsageField(usage map[string]any, names ...string) int64 {
	for _, name := range names {
		value, ok := usage[name]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			if v > 0 {
				return int64(math.Round(v))
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil && parsed > 0 {
				return int64(math.Round(parsed))
			}
		}
	}
	return 0
}

func summarizeSessionReport(sessions []sessionReportRow) ([]sessionReportRepo, sessionReportTotals) {
	byRepo := map[string]*sessionReportRepo{}
	var totals sessionReportTotals
	totals.SessionCount = len(sessions)

	for _, session := range sessions {
		totals.TotalTokens += session.Usage.TotalTokens
		totals.InputTokens += session.Usage.InputTokens
		totals.OutputTokens += session.Usage.OutputTokens
		totals.UsageEvents += session.Usage.UsageEvents

		repos := session.Repos
		if len(repos) == 0 {
			repos = []string{sessionReportUnassigned}
		}
		for _, repo := range repos {
			repo = strings.TrimSpace(repo)
			if repo == "" {
				repo = sessionReportUnassigned
			}
			summary := byRepo[repo]
			if summary == nil {
				summary = &sessionReportRepo{Repo: repo}
				byRepo[repo] = summary
			}
			summary.SessionCount++
			summary.TotalTokens += session.Usage.TotalTokens
			summary.InputTokens += session.Usage.InputTokens
			summary.OutputTokens += session.Usage.OutputTokens
			touched := session.UpdatedAt
			if summary.LastTouched == nil || touched.After(*summary.LastTouched) {
				copied := touched
				summary.LastTouched = &copied
			}
		}
	}

	out := make([]sessionReportRepo, 0, len(byRepo))
	for _, summary := range byRepo {
		out = append(out, *summary)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].TotalTokens > out[i].TotalTokens ||
				(out[j].TotalTokens == out[i].TotalTokens && out[j].SessionCount > out[i].SessionCount) ||
				(out[j].TotalTokens == out[i].TotalTokens && out[j].SessionCount == out[i].SessionCount && out[j].Repo < out[i].Repo) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	totals.RepoCount = len(out)
	return out, totals
}
