package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const debugObservabilitySummaryDescription = `Admin observability inbox for Tank operators.

This endpoint reads Prometheus/Alertmanager state through the in-cluster
Prometheus API and summarizes the signals an operator should check before
falling back to structured logs: firing Tank alerts and recent orchestrator
5xx routes. It intentionally does not store or replay raw logs. Per-entity
detail remains in the existing /api/debug/* surfaces named in debug_links.`

const defaultPrometheusURL = "http://monitoring-kube-prometheus-prometheus.monitoring.svc.cluster.local:9090"

type observabilitySummaryResponse struct {
	Description string                         `json:"description"`
	Status      string                         `json:"status"`
	CheckedAt   string                         `json:"checked_at"`
	Alerts      observabilityAlertSummary      `json:"alerts"`
	HTTP5xx     observabilityHTTP5xxSummary    `json:"http_5xx"`
	DebugLinks  []observabilityDebugLink       `json:"debug_links"`
	Errors      []observabilityCollectionError `json:"errors,omitempty"`
}

type observabilityAlertSummary struct {
	Status           string                   `json:"status"`
	FiringTotal      int                      `json:"firing_total"`
	TankFiringTotal  int                      `json:"tank_firing_total"`
	TankCritical     int                      `json:"tank_critical"`
	TankWarning      int                      `json:"tank_warning"`
	TankInfo         int                      `json:"tank_info"`
	PlatformFiring   int                      `json:"platform_firing"`
	PrometheusURLSet bool                     `json:"prometheus_url_set"`
	Items            []observabilityAlertItem `json:"items"`
}

type observabilityAlertItem struct {
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	Namespace   string `json:"namespace,omitempty"`
	Route       string `json:"route,omitempty"`
	Runbook     string `json:"runbook,omitempty"`
	Description string `json:"description,omitempty"`
	ActiveAt    string `json:"active_at,omitempty"`
}

type observabilityHTTP5xxSummary struct {
	Status string                      `json:"status"`
	Window string                      `json:"window"`
	Total  float64                     `json:"total"`
	Routes []observabilityHTTP5xxRoute `json:"routes"`
}

type observabilityHTTP5xxRoute struct {
	Route string  `json:"route"`
	Count float64 `json:"count"`
}

type observabilityDebugLink struct {
	Label       string `json:"label"`
	Href        string `json:"href"`
	Description string `json:"description"`
}

type observabilityCollectionError struct {
	Surface string `json:"surface"`
	Detail  string `json:"detail"`
}

func (s *appServer) handleDebugObservabilitySummary(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	summary := collectObservabilitySummary(ctx, time.Now().UTC(), prometheusBaseURLFromEnv())
	if len(summary.Errors) > 0 {
		slog.Warn("debug observability summary partially unavailable",
			"caller_email", user.Email,
			"errors", len(summary.Errors),
		)
	}
	writeJSON(w, http.StatusOK, summary)
}

func collectObservabilitySummary(ctx context.Context, now time.Time, prometheusURL string) observabilitySummaryResponse {
	client := &http.Client{Timeout: 1400 * time.Millisecond}
	alerts, alertErr := fetchPrometheusAlerts(ctx, client, prometheusURL)
	http5xx, httpErr := fetchPrometheusHTTP5xx(ctx, client, prometheusURL, "30m")
	var errs []observabilityCollectionError
	if alertErr != nil {
		errs = append(errs, observabilityCollectionError{Surface: "alerts", Detail: alertErr.Error()})
	}
	if httpErr != nil {
		errs = append(errs, observabilityCollectionError{Surface: "http_5xx", Detail: httpErr.Error()})
	}

	alertSummary := summarizeObservabilityAlerts(alerts)
	alertSummary.PrometheusURLSet = strings.TrimSpace(prometheusURL) != ""
	if alertErr != nil {
		alertSummary.Status = "unknown"
	}
	if httpErr != nil {
		http5xx.Status = "unknown"
	}

	status := worstHealthStatus(alertSummary.Status, http5xx.Status)
	if alertErr != nil && httpErr != nil {
		status = "unknown"
	}

	return observabilitySummaryResponse{
		Description: debugObservabilitySummaryDescription,
		Status:      status,
		CheckedAt:   now.Format(time.RFC3339Nano),
		Alerts:      alertSummary,
		HTTP5xx:     http5xx,
		DebugLinks:  observabilityDebugLinks(),
		Errors:      errs,
	}
}

func prometheusBaseURLFromEnv() string {
	return strings.TrimRight(envDefault("PROMETHEUS_URL", defaultPrometheusURL), "/")
}

func fetchPrometheusAlerts(ctx context.Context, client *http.Client, prometheusURL string) ([]observabilityAlertItem, error) {
	if strings.TrimSpace(prometheusURL) == "" {
		return nil, fmt.Errorf("Prometheus URL is not configured")
	}
	var body prometheusAlertsResponse
	if err := fetchPrometheusJSON(ctx, client, prometheusURL, "/api/v1/alerts", &body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("Prometheus alerts status %q", body.Status)
	}
	items := make([]observabilityAlertItem, 0, len(body.Data.Alerts))
	for _, alert := range body.Data.Alerts {
		if strings.TrimSpace(alert.State) != "firing" {
			continue
		}
		name := strings.TrimSpace(alert.Labels["alertname"])
		if name == "" {
			name = "unknown"
		}
		item := observabilityAlertItem{
			Name:        name,
			Severity:    severityLabel(alert.Labels["severity"]),
			State:       "firing",
			Namespace:   strings.TrimSpace(alert.Labels["namespace"]),
			Route:       strings.TrimSpace(alert.Labels["route"]),
			Runbook:     strings.TrimSpace(alert.Annotations["runbook"]),
			Description: strings.TrimSpace(firstNonEmptyAnnotation(alert.Annotations["description"], alert.Annotations["summary"])),
			ActiveAt:    strings.TrimSpace(alert.ActiveAt),
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if severityRank(items[i].Severity) != severityRank(items[j].Severity) {
			return severityRank(items[i].Severity) > severityRank(items[j].Severity)
		}
		return items[i].Name < items[j].Name
	})
	if len(items) > 40 {
		items = items[:40]
	}
	return items, nil
}

func summarizeObservabilityAlerts(items []observabilityAlertItem) observabilityAlertSummary {
	out := observabilityAlertSummary{
		Status:      "healthy",
		FiringTotal: len(items),
		Items:       items,
	}
	for _, item := range items {
		if strings.HasPrefix(item.Name, "Tank") {
			out.TankFiringTotal++
			switch item.Severity {
			case "critical":
				out.TankCritical++
			case "warning":
				out.TankWarning++
			case "info":
				out.TankInfo++
			}
			continue
		}
		out.PlatformFiring++
	}
	switch {
	case out.TankCritical > 0:
		out.Status = "critical"
	case out.TankWarning > 0:
		out.Status = "warning"
	default:
		out.Status = "healthy"
	}
	return out
}

func fetchPrometheusHTTP5xx(ctx context.Context, client *http.Client, prometheusURL, window string) (observabilityHTTP5xxSummary, error) {
	out := observabilityHTTP5xxSummary{
		Status: "healthy",
		Window: window,
		Routes: []observabilityHTTP5xxRoute{},
	}
	if strings.TrimSpace(prometheusURL) == "" {
		return out, fmt.Errorf("Prometheus URL is not configured")
	}
	query := fmt.Sprintf(`sum by (route) (increase(tank_http_requests_total{status_class="5xx"}[%s]))`, window)
	var body prometheusQueryResponse
	path := "/api/v1/query?query=" + url.QueryEscape(query)
	if err := fetchPrometheusJSON(ctx, client, prometheusURL, path, &body); err != nil {
		return out, err
	}
	if body.Status != "success" {
		return out, fmt.Errorf("Prometheus query status %q", body.Status)
	}
	for _, result := range body.Data.Result {
		if len(result.Value) < 2 {
			continue
		}
		raw, _ := result.Value[1].(string)
		count, err := strconv.ParseFloat(raw, 64)
		if err != nil || count <= 0 {
			continue
		}
		count = roundMetric(count)
		route := strings.TrimSpace(result.Metric["route"])
		if route == "" {
			route = "<unknown>"
		}
		out.Total += count
		out.Routes = append(out.Routes, observabilityHTTP5xxRoute{
			Route: route,
			Count: count,
		})
	}
	sort.SliceStable(out.Routes, func(i, j int) bool {
		if out.Routes[i].Count != out.Routes[j].Count {
			return out.Routes[i].Count > out.Routes[j].Count
		}
		return out.Routes[i].Route < out.Routes[j].Route
	})
	if len(out.Routes) > 8 {
		out.Routes = out.Routes[:8]
	}
	out.Total = roundMetric(out.Total)
	if out.Total > 0 {
		out.Status = "warning"
	}
	return out, nil
}

func fetchPrometheusJSON(ctx context.Context, client *http.Client, baseURL, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Prometheus returned %s", res.Status)
	}
	dec := json.NewDecoder(res.Body)
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("decode Prometheus response: %w", err)
	}
	return nil
}

func observabilityDebugLinks() []observabilityDebugLink {
	return []observabilityDebugLink{
		{
			Label:       "Session event streams",
			Href:        "/api/debug/session-event-streams",
			Description: "Open SSE stream wake/page/emit state for live transcript delivery.",
		},
		{
			Label:       "Session event ledger",
			Href:        "/api/debug/session-event-ledger?session_id=",
			Description: "Raw durable session_events rows for one session.",
		},
		{
			Label:       "Conversation read state",
			Href:        "/api/debug/conversation-read-state?session_id=",
			Description: "Per-session read cursor and durable activity-summary state.",
		},
		{
			Label:       "Session-list captures",
			Href:        "/api/debug/session-list-captures",
			Description: "Durable browser-side session-list diagnostic captures.",
		},
		{
			Label:       "Avatar upload attempts",
			Href:        "/api/debug/avatar-upload-attempts",
			Description: "Reference-id lookup for admin avatar upload failures.",
		},
	}
}

func severityLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "warning", "info":
		return strings.ToLower(strings.TrimSpace(raw))
	case "none":
		return "info"
	default:
		return "unknown"
	}
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func roundMetric(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func firstNonEmptyAnnotation(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type prometheusAlertsResponse struct {
	Status string `json:"status"`
	Data   struct {
		Alerts []struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			State       string            `json:"state"`
			ActiveAt    string            `json:"activeAt"`
		} `json:"alerts"`
	} `json:"data"`
}

type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}
