package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type providerQuotaProxyResponse struct {
	Provider   string         `json:"provider"`
	Status     string         `json:"status"`
	StatusCode *int           `json:"status_code,omitempty"`
	Error      string         `json:"error,omitempty"`
	ObservedAt string         `json:"observed_at,omitempty"`
	Usage      map[string]any `json:"usage,omitempty"`
}

type providerQuotaResponse struct {
	ObservedAt string            `json:"observed_at,omitempty"`
	RateLimits []map[string]any  `json:"rate_limits"`
	Errors     map[string]string `json:"errors,omitempty"`
	SourceURLs map[string]string `json:"source_urls,omitempty"`
}

const defaultProviderQuotaRefreshInterval = time.Hour

func (s *appServer) handleProviderQuotas(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	sources := map[string]string{
		"anthropic": envDefault("CLAUDE_PROVIDER_USAGE_URL", s.defaultProviderUsageURL("claude")),
		"anthropic_secondary": envDefault(
			"CLAUDE_SECONDARY_PROVIDER_USAGE_URL",
			s.defaultProviderUsageURL("claude_secondary"),
		),
		"codex": envDefault("CODEX_PROVIDER_USAGE_URL", s.defaultProviderUsageURL("codex")),
	}
	out := providerQuotaResponse{
		RateLimits: []map[string]any{},
		Errors:     map[string]string{},
		SourceURLs: map[string]string{},
	}
	scope := s.providerQuotaScope()
	liveRows := []map[string]any{}
	latestObserved := ""
	durableRows := []map[string]any{}
	durableLoaded := false
	wroteLiveRows := false
	freshProviders := map[string]time.Time{}
	if s.pgPool != nil {
		rows, err := s.loadProviderQuotaSnapshots(ctx, scope)
		if err != nil {
			out.Errors["_durable"] = "durable quota snapshot read failed"
			slog.Error("provider quota snapshot read failed", "scope", scope, "error", err)
		} else {
			durableRows = rows
			durableLoaded = true
			freshProviders = freshProviderQuotaProviders(rows, time.Now().UTC(), providerQuotaRefreshInterval())
			for _, row := range rows {
				if observedAt, ok := stringish(row["observedAt"]); ok && observedAt != "" &&
					(latestObserved == "" || observedAfter(observedAt, latestObserved)) {
					latestObserved = observedAt
				}
			}
		}
	}
	for provider, url := range sources {
		if observedAt, ok := freshProviders[provider]; ok {
			slog.Info("provider quota source skipped; durable snapshot is fresh",
				"provider", provider,
				"observed_at", observedAt.Format(time.RFC3339),
			)
			continue
		}
		url = strings.TrimSpace(url)
		if url == "" {
			out.Errors[provider] = "usage source not configured"
			continue
		}
		out.SourceURLs[provider] = url
		snapshot, err := fetchProviderQuotaProxy(ctx, url)
		if err != nil {
			out.Errors[provider] = err.Error()
			slog.Info("provider quota source failed",
				"provider", provider,
				"error", err.Error(),
			)
			continue
		}
		if snapshot.Status != "ok" {
			sourceError := fmt.Sprintf("usage source returned status %q", snapshot.Status)
			if snapshot.Error != "" {
				out.Errors[provider] = snapshot.Error
				sourceError = snapshot.Error
			} else {
				out.Errors[provider] = sourceError
			}
			slog.Info("provider quota source non-ok",
				"provider", provider,
				"status", snapshot.Status,
				"status_code", snapshot.StatusCode,
				"error", sourceError,
			)
			continue
		}
		observedAt := normalizeObservedAt(snapshot.ObservedAt)
		if observedAt == "" {
			observedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if latestObserved == "" || observedAfter(observedAt, latestObserved) {
			latestObserved = observedAt
		}
		rows := providerUsageEvidence(provider, observedAt, snapshot.Usage)
		if len(rows) > 0 {
			if err := s.upsertProviderQuotaSnapshots(ctx, scope, rows, "provider_usage_proxy"); err != nil {
				out.Errors[provider] = "durable quota snapshot write failed"
				slog.Error("provider quota snapshot write failed",
					"provider", provider,
					"rows", len(rows),
					"error", err,
				)
			}
			liveRows = append(liveRows, rows...)
			wroteLiveRows = true
		}
		slog.Info("provider quota source ok",
			"provider", provider,
			"rows", len(rows),
			"observed_at", observedAt,
			"status_code", snapshot.StatusCode,
		)
	}
	if s.pgPool != nil {
		rows := durableRows
		var err error
		if !durableLoaded || wroteLiveRows {
			rows, err = s.loadProviderQuotaSnapshots(ctx, scope)
		}
		if err != nil {
			out.Errors["_durable"] = "durable quota snapshot read failed"
			out.RateLimits = liveRows
			slog.Error("provider quota snapshot read failed", "scope", scope, "error", err)
		} else {
			out.RateLimits = rows
			for _, row := range rows {
				if observedAt, ok := stringish(row["observedAt"]); ok && observedAt != "" &&
					(latestObserved == "" || observedAfter(observedAt, latestObserved)) {
					latestObserved = observedAt
				}
			}
		}
	} else {
		out.RateLimits = liveRows
	}
	out.ObservedAt = latestObserved
	if len(out.Errors) == 0 {
		out.Errors = nil
	}
	if len(out.SourceURLs) == 0 {
		out.SourceURLs = nil
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *appServer) providerQuotaScope() string {
	scope := strings.TrimSpace(s.sessionScope)
	if scope == "" {
		return "default"
	}
	return scope
}

func (s *appServer) upsertProviderQuotaSnapshots(ctx context.Context, scope string, rows []map[string]any, source string) error {
	if s.pgPool == nil || len(rows) == 0 {
		return nil
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "unknown"
	}
	const q = `
		INSERT INTO provider_quota_snapshots
			(session_scope, provider, window_id, status, utilization, resets_at, observed_at, source, raw, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (session_scope, provider, window_id) DO UPDATE
		SET status = EXCLUDED.status,
			utilization = EXCLUDED.utilization,
			resets_at = EXCLUDED.resets_at,
			observed_at = EXCLUDED.observed_at,
			source = EXCLUDED.source,
			raw = EXCLUDED.raw,
			updated_at = now()
		WHERE provider_quota_snapshots.observed_at <= EXCLUDED.observed_at
	`
	for _, row := range rows {
		provider, _ := stringish(row["provider"])
		windowID, _ := stringish(row["rateLimitType"])
		status, _ := stringish(row["status"])
		if status == "" {
			status = "ok"
		}
		observedAt, _ := stringish(row["observedAt"])
		observed := parseProviderQuotaObservedAt(observedAt)
		if observed.IsZero() {
			observed = time.Now().UTC()
		}
		if !validProviderQuotaProvider(provider) || !validProviderQuotaWindow(windowID) {
			continue
		}
		var utilization any
		if v, ok := numericAny(row["utilization"]); ok {
			utilization = v
		}
		var resetsAt any
		if v := row["resetsAt"]; v != nil {
			resetsAt = fmt.Sprint(v)
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := s.pgPool.Exec(ctx, q, scope, provider, windowID, status, utilization, resetsAt, observed, source, raw); err != nil {
			return err
		}
	}
	return nil
}

func (s *appServer) loadProviderQuotaSnapshots(ctx context.Context, scope string) ([]map[string]any, error) {
	if s.pgPool == nil {
		return nil, nil
	}
	const q = `
		SELECT provider, window_id, status, utilization, resets_at,
			to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS observed_at,
			source
		FROM provider_quota_snapshots
		WHERE session_scope = $1
		ORDER BY provider, window_id
	`
	rows, err := s.pgPool.Query(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var provider, windowID, status, observedAt, source string
		var utilization sql.NullFloat64
		var resetsAt sql.NullString
		if err := rows.Scan(&provider, &windowID, &status, &utilization, &resetsAt, &observedAt, &source); err != nil {
			return nil, err
		}
		row := map[string]any{
			"provider":      provider,
			"rateLimitType": windowID,
			"status":        status,
			"observedAt":    observedAt,
			"source":        source,
		}
		if utilization.Valid {
			row["utilization"] = utilization.Float64
		}
		if resetsAt.Valid && resetsAt.String != "" {
			row["resetsAt"] = resetsAt.String
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func providerQuotaRefreshInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("PROVIDER_QUOTA_REFRESH_INTERVAL"))
	if raw == "" {
		return defaultProviderQuotaRefreshInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		slog.Warn("invalid PROVIDER_QUOTA_REFRESH_INTERVAL; using default", "value", raw)
		return defaultProviderQuotaRefreshInterval
	}
	return d
}

func freshProviderQuotaProviders(rows []map[string]any, now time.Time, maxAge time.Duration) map[string]time.Time {
	fresh := map[string]time.Time{}
	if maxAge <= 0 || now.IsZero() {
		return fresh
	}
	for _, row := range rows {
		provider, _ := stringish(row["provider"])
		if !validProviderQuotaProvider(provider) {
			continue
		}
		observedAt, _ := stringish(row["observedAt"])
		observed := parseProviderQuotaObservedAt(observedAt)
		if observed.IsZero() || now.Sub(observed) > maxAge {
			continue
		}
		if current, ok := fresh[provider]; !ok || observed.After(current) {
			fresh[provider] = observed
		}
	}
	return fresh
}

func (s *appServer) defaultProviderUsageURL(provider string) string {
	if host, path := providerUsageHostAndPath(provider); host != "" {
		return fmt.Sprintf("http://%s:9100/usage/%s", host, path)
	}
	ns := strings.TrimSpace(os.Getenv("PROVIDER_USAGE_NAMESPACE"))
	if ns == "" {
		ns = providerUsageNamespace(s.namespace)
	}
	switch provider {
	case "claude":
		return fmt.Sprintf("http://claude-api-proxy.%s.svc.cluster.local:9100/usage/claude", ns)
	case "claude_secondary":
		return fmt.Sprintf("http://claude-secondary-api-proxy.%s.svc.cluster.local:9100/usage/claude", ns)
	case "codex":
		return fmt.Sprintf("http://codex-api-proxy.%s.svc.cluster.local:9100/usage/codex", ns)
	default:
		return ""
	}
}

func providerUsageHostAndPath(provider string) (string, string) {
	var host string
	var path string
	switch provider {
	case "claude":
		host = os.Getenv("CLAUDE_API_PROXY_HOST")
		path = "claude"
	case "claude_secondary":
		host = os.Getenv("CLAUDE_SECONDARY_API_PROXY_HOST")
		path = "claude"
	case "codex":
		host = os.Getenv("CODEX_API_PROXY_HOST")
		path = "codex"
	default:
		return "", ""
	}
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if before, _, ok := strings.Cut(host, "/"); ok {
		host = before
	}
	if before, _, ok := strings.Cut(host, ":"); ok {
		host = before
	}
	return host, path
}

func providerUsageNamespace(sessionsNamespace string) string {
	ns := strings.TrimSpace(sessionsNamespace)
	if ns == "" {
		return "tank-operator"
	}
	if strings.HasSuffix(ns, "-sessions") {
		return strings.TrimSuffix(ns, "-sessions")
	}
	return ns
}

func fetchProviderQuotaProxy(ctx context.Context, url string) (providerQuotaProxyResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return providerQuotaProxyResponse{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return providerQuotaProxyResponse{}, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return providerQuotaProxyResponse{}, readErr
	}
	var parsed providerQuotaProxyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return providerQuotaProxyResponse{}, fmt.Errorf("usage source returned non-json status %d", resp.StatusCode)
	}
	if parsed.StatusCode == nil {
		status := resp.StatusCode
		parsed.StatusCode = &status
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if parsed.Error != "" {
			return parsed, nil
		}
		return parsed, fmt.Errorf("usage source returned status %d", resp.StatusCode)
	}
	return parsed, nil
}

func providerUsageEvidence(provider, observedAt string, raw map[string]any) []map[string]any {
	best := map[string]map[string]any{}
	var walk func(path string, value any)
	walk = func(path string, value any) {
		switch node := value.(type) {
		case map[string]any:
			nextPath := path
			for _, key := range []string{"rateLimitType", "rate_limit_type", "window", "name", "bucket", "id", "type"} {
				if name, ok := stringish(node[key]); ok && name != "" {
					nextPath = strings.TrimSpace(path + " " + name)
					break
				}
			}
			if utilization, ok := quotaUtilization(node); ok {
				windowID := quotaWindowID(nextPath)
				if windowID != "" {
					evidence := map[string]any{
						"provider":      provider,
						"status":        "ok",
						"rateLimitType": windowID,
						"utilization":   utilization,
						"source":        "provider_usage_proxy",
					}
					if reset := quotaResetValue(node); reset != nil {
						evidence["resetsAt"] = reset
					}
					if observedAt != "" {
						evidence["observedAt"] = observedAt
					}
					keepBestQuotaEvidence(best, provider+":"+windowID, evidence)
				}
			}
			for key, child := range node {
				walk(strings.TrimSpace(nextPath+" "+key), child)
			}
		case []any:
			for _, child := range node {
				walk(path, child)
			}
		}
	}
	walk("", raw)
	out := make([]map[string]any, 0, len(best))
	for _, key := range []string{provider + ":five_hour", provider + ":weekly", provider + ":opus_weekly"} {
		if evidence := best[key]; evidence != nil {
			out = append(out, evidence)
		}
	}
	return out
}

func keepBestQuotaEvidence(best map[string]map[string]any, key string, next map[string]any) {
	current := best[key]
	if current == nil {
		best[key] = next
		return
	}
	currentUtil, _ := numericAny(current["utilization"])
	nextUtil, _ := numericAny(next["utilization"])
	if nextUtil >= currentUtil {
		best[key] = next
	}
}

func quotaUtilization(node map[string]any) (float64, bool) {
	for _, key := range []string{
		"utilization",
		"utilization_pct",
		"utilizationPercent",
		"used_percent",
		"usedPercent",
		"usage_percent",
		"usagePercent",
		"percent_used",
		"percentUsed",
	} {
		if value, ok := numericAny(node[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func quotaResetValue(node map[string]any) any {
	for _, key := range []string{"resetsAt", "resets_at", "resetAt", "reset_at"} {
		if value := node[key]; value != nil {
			return value
		}
	}
	return nil
}

func quotaWindowID(path string) string {
	normalized := strings.ToLower(path)
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if strings.Contains(normalized, "opus") {
		return "opus_weekly"
	}
	if strings.Contains(normalized, "five_hour") ||
		strings.Contains(normalized, "5_hour") ||
		strings.Contains(normalized, "5h") ||
		strings.Contains(normalized, "primary_window") ||
		strings.HasSuffix(normalized, "primary") ||
		(strings.Contains(normalized, "five") && strings.Contains(normalized, "hour")) {
		return "five_hour"
	}
	if strings.Contains(normalized, "seven_day") ||
		strings.Contains(normalized, "7_day") ||
		strings.Contains(normalized, "7d") ||
		strings.Contains(normalized, "week") ||
		strings.Contains(normalized, "weekly") ||
		strings.Contains(normalized, "secondary_window") ||
		strings.HasSuffix(normalized, "secondary") {
		return "weekly"
	}
	return ""
}

func numericAny(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func stringish(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case fmt.Stringer:
		return v.String(), true
	default:
		return "", false
	}
}

func normalizeObservedAt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(time.RFC3339)
}

func observedAfter(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a > b
	}
	return ta.After(tb)
}

func parseProviderQuotaObservedAt(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func validProviderQuotaProvider(provider string) bool {
	switch provider {
	case "anthropic", "anthropic_secondary", "codex":
		return true
	default:
		return false
	}
}

func validProviderQuotaWindow(windowID string) bool {
	switch windowID {
	case "five_hour", "weekly", "opus_weekly":
		return true
	default:
		return false
	}
}
