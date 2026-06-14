package main

import (
	"context"
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

type providerQuotaCachedEvidence struct {
	ObservedAt string
	RateLimits []map[string]any
	StoredAt   time.Time
}

const providerQuotaCacheMaxAge = 30 * time.Minute

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
	latestObserved := ""
	for provider, url := range sources {
		url = strings.TrimSpace(url)
		if url == "" {
			out.Errors[provider] = "usage source not configured"
			continue
		}
		out.SourceURLs[provider] = url
		snapshot, err := fetchProviderQuotaProxy(ctx, url)
		if err != nil {
			out.Errors[provider] = err.Error()
			if rows, observedAt, ok := s.cachedProviderQuotaEvidence(provider, time.Now().UTC()); ok {
				slog.Info("provider quota source using cached rows",
					"provider", provider,
					"rows", len(rows),
					"observed_at", observedAt,
					"error", err.Error(),
				)
				out.RateLimits = append(out.RateLimits, rows...)
				if latestObserved == "" || observedAfter(observedAt, latestObserved) {
					latestObserved = observedAt
				}
			} else {
				slog.Info("provider quota source failed without cache",
					"provider", provider,
					"error", err.Error(),
				)
			}
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
			if rows, observedAt, ok := s.cachedProviderQuotaEvidence(provider, time.Now().UTC()); ok {
				slog.Info("provider quota source using cached rows",
					"provider", provider,
					"rows", len(rows),
					"observed_at", observedAt,
					"status", snapshot.Status,
					"status_code", snapshot.StatusCode,
					"error", sourceError,
				)
				out.RateLimits = append(out.RateLimits, rows...)
				if latestObserved == "" || observedAfter(observedAt, latestObserved) {
					latestObserved = observedAt
				}
			} else {
				slog.Info("provider quota source non-ok without cache",
					"provider", provider,
					"status", snapshot.Status,
					"status_code", snapshot.StatusCode,
					"error", sourceError,
				)
			}
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
			s.rememberProviderQuotaEvidence(provider, observedAt, rows, time.Now().UTC())
		}
		slog.Info("provider quota source ok",
			"provider", provider,
			"rows", len(rows),
			"observed_at", observedAt,
			"status_code", snapshot.StatusCode,
		)
		out.RateLimits = append(out.RateLimits, rows...)
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

func (s *appServer) rememberProviderQuotaEvidence(provider, observedAt string, rows []map[string]any, now time.Time) {
	if provider == "" || len(rows) == 0 {
		return
	}
	s.providerQuotaMu.Lock()
	defer s.providerQuotaMu.Unlock()
	if s.providerQuotaCache == nil {
		s.providerQuotaCache = map[string]providerQuotaCachedEvidence{}
	}
	s.providerQuotaCache[provider] = providerQuotaCachedEvidence{
		ObservedAt: observedAt,
		RateLimits: copyProviderQuotaRows(rows, false),
		StoredAt:   now,
	}
}

func (s *appServer) cachedProviderQuotaEvidence(provider string, now time.Time) ([]map[string]any, string, bool) {
	s.providerQuotaMu.Lock()
	defer s.providerQuotaMu.Unlock()
	cached, ok := s.providerQuotaCache[provider]
	if !ok || len(cached.RateLimits) == 0 {
		return nil, "", false
	}
	if !cached.StoredAt.IsZero() && now.Sub(cached.StoredAt) > providerQuotaCacheMaxAge {
		delete(s.providerQuotaCache, provider)
		return nil, "", false
	}
	return copyProviderQuotaRows(cached.RateLimits, true), cached.ObservedAt, true
}

func copyProviderQuotaRows(rows []map[string]any, cached bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := make(map[string]any, len(row)+1)
		for key, value := range row {
			next[key] = value
		}
		if cached {
			next["cached"] = true
		}
		out = append(out, next)
	}
	return out
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
