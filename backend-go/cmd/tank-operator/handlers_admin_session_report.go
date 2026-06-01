package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

const (
	sessionReportDefaultDays  = 30
	sessionReportMaxDays      = 365
	sessionReportDefaultLimit = 750
	sessionReportMaxLimit     = 2000
	sessionReportUnassigned   = "Unassigned"
	sessionReportDateLayout   = "2006-01-02"
	sessionReportShareSession = "__session_report__"
	sessionReportSharePrefix  = "session-report:v2:"
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

type sessionReportWindow struct {
	Days     int
	StartsAt time.Time
	EndsAt   time.Time
}

type sessionReportSharePayload struct {
	Version int             `json:"version"`
	Report  json.RawMessage `json:"report"`
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
	window, ok := sessionReportWindowFromRequest(w, r, time.Now().UTC())
	if !ok {
		return
	}
	limit, ok := boundedPositiveIntQuery(w, r, "limit", sessionReportDefaultLimit, sessionReportMaxLimit)
	if !ok {
		return
	}

	s.writeSessionReport(w, r, scope, window, limit, nil)
}

func (s *appServer) handleCreateSessionReportShare(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.messageLinkShares == nil {
		writeError(w, http.StatusServiceUnavailable, "public share store not configured")
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
	window, ok := sessionReportWindowFromRequest(w, r, time.Now().UTC())
	if !ok {
		return
	}
	limit, ok := boundedPositiveIntQuery(w, r, "limit", sessionReportDefaultLimit, sessionReportMaxLimit)
	if !ok {
		return
	}

	var token string
	var err error
	createdAt := time.Now().UTC()
	for attempt := 0; attempt < 3; attempt++ {
		token = auth.RandomHex(messageLinkShareTokenBytes)
		snapshot, snapshotErr := s.buildSessionReportBody(r.Context(), scope, window, limit, map[string]any{
			"token":      token,
			"created_at": createdAt.Format(time.RFC3339Nano),
			"snapshot":   true,
		}, createdAt)
		if snapshotErr != nil {
			writeError(w, http.StatusInternalServerError, snapshotErr.Error())
			return
		}
		payload, payloadErr := encodeSessionReportShareSnapshot(snapshot)
		if payloadErr != nil {
			writeError(w, http.StatusInternalServerError, payloadErr.Error())
			return
		}
		err = s.messageLinkShares.Create(r.Context(), pgstore.MessageLinkShare{
			Token:        token,
			CreatedBy:    user.OwnerEmail(),
			OwnerEmail:   user.OwnerEmail(),
			SessionScope: scope,
			SessionID:    sessionReportShareSession,
			TimelineID:   payload,
		})
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "collision") {
			break
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	browserURL := sessionReportShareBrowserURL(r, token)
	writeJSON(w, http.StatusCreated, map[string]any{
		"kind":        "tank.session_report_share",
		"version":     2,
		"token":       token,
		"browser_url": browserURL,
		"public_url": absoluteURL(requestOrigin(r), &url.URL{
			Path: "/api/public/session-report-shares/" + url.PathEscape(token),
		}),
		"scope":  scope,
		"range":  sessionReportWindowBody(window),
		"limit":  limit,
		"copied": false,
	})
}

func (s *appServer) handleGetPublicSessionReportShare(w http.ResponseWriter, r *http.Request) {
	if s.messageLinkShares == nil {
		writeError(w, http.StatusServiceUnavailable, "public share store not configured")
		return
	}
	share, err := s.messageLinkShares.Get(r.Context(), r.PathValue("share_token"))
	if err != nil {
		if errors.Is(err, pgstore.ErrMessageLinkShareInvalid) {
			writeError(w, http.StatusNotFound, "session report share not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot, err := decodeSessionReportShareSnapshot(share)
	if err != nil {
		writeError(w, http.StatusNotFound, "session report share not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snapshot)
}

func (s *appServer) writeSessionReport(w http.ResponseWriter, r *http.Request, scope string, window sessionReportWindow, limit int, share map[string]any) {
	body, err := s.buildSessionReportBody(r.Context(), scope, window, limit, share, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) buildSessionReportBody(ctx context.Context, scope string, window sessionReportWindow, limit int, share map[string]any, fetchedAt time.Time) (map[string]any, error) {
	sessions, err := fetchSessionReportRows(ctx, s, scope, window, limit)
	if err != nil {
		return nil, fmt.Errorf("session report: %w", err)
	}
	if err := attachSessionReportUsage(ctx, s, scope, sessions); err != nil {
		return nil, fmt.Errorf("session report usage: %w", err)
	}

	repos, totals := summarizeSessionReport(sessions)
	return map[string]any{
		"description": "Cheap draft report over recent sessions. Repo attribution uses create-time sessions.repos only.",
		"scope":       scope,
		"days":        window.Days,
		"range":       sessionReportWindowBody(window),
		"limit":       limit,
		"attribution": "A session's latest-per-turn usage is credited to every selected repo on that session; multi-repo sessions intentionally appear in multiple repo buckets.",
		"totals":      totals,
		"repos":       repos,
		"sessions":    sessions,
		"share":       share,
		"fetched_at":  fetchedAt.UTC().Format(time.RFC3339Nano),
	}, nil
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

func sessionReportWindowFromRequest(w http.ResponseWriter, r *http.Request, now time.Time) (sessionReportWindow, bool) {
	q := r.URL.Query()
	fromRaw := strings.TrimSpace(q.Get("from"))
	toRaw := strings.TrimSpace(q.Get("to"))
	if fromRaw != "" || toRaw != "" {
		if fromRaw == "" || toRaw == "" {
			writeError(w, http.StatusBadRequest, "from and to are both required for a custom report range")
			return sessionReportWindow{}, false
		}
		startsAt, ok := parseSessionReportBound(w, "from", fromRaw, false)
		if !ok {
			return sessionReportWindow{}, false
		}
		endsAt, ok := parseSessionReportBound(w, "to", toRaw, true)
		if !ok {
			return sessionReportWindow{}, false
		}
		if !endsAt.After(startsAt) {
			writeError(w, http.StatusBadRequest, "to must be after from")
			return sessionReportWindow{}, false
		}
		if endsAt.Sub(startsAt) > sessionReportMaxDays*24*time.Hour {
			writeError(w, http.StatusBadRequest, "custom report range cannot exceed 365 days")
			return sessionReportWindow{}, false
		}
		return sessionReportWindow{StartsAt: startsAt, EndsAt: endsAt}, true
	}
	days, ok := boundedPositiveIntQuery(w, r, "days", sessionReportDefaultDays, sessionReportMaxDays)
	if !ok {
		return sessionReportWindow{}, false
	}
	now = now.UTC()
	return sessionReportWindow{
		Days:     days,
		StartsAt: now.AddDate(0, 0, -days),
		EndsAt:   now,
	}, true
}

func parseSessionReportBound(w http.ResponseWriter, name, raw string, endOfDate bool) (time.Time, bool) {
	if parsed, err := time.Parse(sessionReportDateLayout, raw); err == nil {
		parsed = parsed.UTC()
		if endOfDate {
			parsed = parsed.AddDate(0, 0, 1)
		}
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, name+" must be YYYY-MM-DD or RFC3339")
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func sessionReportWindowBody(window sessionReportWindow) map[string]any {
	mode := "custom"
	if window.Days > 0 {
		mode = "last_days"
	}
	body := map[string]any{
		"mode":      mode,
		"starts_at": window.StartsAt.UTC().Format(time.RFC3339Nano),
		"ends_at":   window.EndsAt.UTC().Format(time.RFC3339Nano),
		"label":     sessionReportWindowLabel(window),
	}
	if window.Days > 0 {
		body["days"] = window.Days
	}
	return body
}

func sessionReportWindowLabel(window sessionReportWindow) string {
	if window.Days == 1 {
		return "Last 1 day"
	}
	if window.Days > 1 {
		return "Last " + strconv.Itoa(window.Days) + " days"
	}
	start := window.StartsAt.UTC().Format(sessionReportDateLayout)
	end := window.EndsAt.UTC().Add(-time.Nanosecond).Format(sessionReportDateLayout)
	if start == end {
		return start
	}
	return start + " to " + end
}

func sessionReportShareBrowserURL(r *http.Request, token string) string {
	return absoluteURL(requestOrigin(r), &url.URL{
		Path:     "/reports/session-repo",
		RawQuery: url.Values{"share": []string{token}}.Encode(),
	})
}

func encodeSessionReportShareSnapshot(report map[string]any) (string, error) {
	reportBody, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(sessionReportSharePayload{
		Version: 2,
		Report:  reportBody,
	})
	if err != nil {
		return "", err
	}
	return sessionReportSharePrefix + base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeSessionReportShareSnapshot(share pgstore.MessageLinkShare) (json.RawMessage, error) {
	if share.SessionID != sessionReportShareSession || !strings.HasPrefix(share.TimelineID, sessionReportSharePrefix) {
		return nil, errors.New("not a session report share")
	}
	encoded := strings.TrimPrefix(share.TimelineID, sessionReportSharePrefix)
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var payload sessionReportSharePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Version != 2 || len(payload.Report) == 0 || !json.Valid(payload.Report) {
		return nil, errors.New("invalid session report share snapshot")
	}
	return payload.Report, nil
}

func fetchSessionReportRows(ctx context.Context, s *appServer, scope string, window sessionReportWindow, limit int) ([]sessionReportRow, error) {
	const q = `
		SELECT email, session_id, COALESCE(name, ''), mode, COALESCE(repos, '{}'::text[]),
		       visible, created_at, updated_at
		FROM sessions
		WHERE session_scope = $1
		  AND created_at >= $2
		  AND created_at < $3
		ORDER BY created_at DESC
		LIMIT $4
	`
	rows, err := s.pgPool.Query(ctx, q, scope, window.StartsAt, window.EndsAt, limit)
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
