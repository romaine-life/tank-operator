package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
)

func TestSessionReportWindowFromRequestSupportsOneDay(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-report?days=1", nil)
	rec := httptest.NewRecorder()

	window, ok := sessionReportWindowFromRequest(rec, req, now)
	if !ok {
		t.Fatalf("parse failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if window.Days != 1 || !window.StartsAt.Equal(now.AddDate(0, 0, -1)) || !window.EndsAt.Equal(now) {
		t.Fatalf("window = %+v", window)
	}
	if got := sessionReportWindowLabel(window); got != "Last 1 day" {
		t.Fatalf("label = %q", got)
	}
}

func TestSessionReportWindowFromRequestSupportsCustomDateRange(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-report?from=2026-05-30&to=2026-06-01", nil)
	rec := httptest.NewRecorder()

	window, ok := sessionReportWindowFromRequest(rec, req, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatalf("parse failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	wantStart := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if window.Days != 0 || !window.StartsAt.Equal(wantStart) || !window.EndsAt.Equal(wantEnd) {
		t.Fatalf("window = %+v, want start=%s end=%s", window, wantStart, wantEnd)
	}
	if got := sessionReportWindowLabel(window); got != "2026-05-30 to 2026-06-01" {
		t.Fatalf("label = %q", got)
	}
}

func TestSessionReportSharePayloadRoundTripsWindow(t *testing.T) {
	window := sessionReportWindow{
		StartsAt: time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	}
	payload, err := encodeSessionReportSharePayload("tank-operator-slot-1", window, 250)
	if err != nil {
		t.Fatal(err)
	}

	scope, got, limit, err := decodeSessionReportShare(pgstore.MessageLinkShare{
		SessionID:  sessionReportShareSession,
		TimelineID: payload,
	}, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if scope != "tank-operator-slot-1" || limit != 250 || got.Days != 0 || !got.StartsAt.Equal(window.StartsAt) || !got.EndsAt.Equal(window.EndsAt) {
		t.Fatalf("decoded = scope:%q window:%+v limit:%d", scope, got, limit)
	}
}

func TestSessionReportSharePayloadRoundTripsRelativeDays(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	payload, err := encodeSessionReportSharePayload("default", sessionReportWindow{Days: 1}, 100)
	if err != nil {
		t.Fatal(err)
	}

	scope, got, limit, err := decodeSessionReportShare(pgstore.MessageLinkShare{
		SessionID:  sessionReportShareSession,
		TimelineID: payload,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if scope != "default" || limit != 100 || got.Days != 1 || !got.StartsAt.Equal(now.AddDate(0, 0, -1)) || !got.EndsAt.Equal(now) {
		t.Fatalf("decoded = scope:%q window:%+v limit:%d", scope, got, limit)
	}
}

func TestTokenUsageFromEventPayload(t *testing.T) {
	payload := []byte(`{
		"payload": {
			"usage": {
				"input_tokens": 120,
				"output_tokens": 30,
				"total_tokens": 150
			}
		}
	}`)

	got := tokenUsageFromEventPayload(payload)
	if got.TotalTokens != 150 || got.InputTokens != 120 || got.OutputTokens != 30 {
		t.Fatalf("usage = %+v, want total=150 input=120 output=30", got)
	}
}

func TestSummarizeSessionReportCreditsSelectedRepos(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sessions := []sessionReportRow{
		{
			SessionID: "1",
			Repos:     []string{"nelsong6/tank-operator", "nelsong6/glimmung"},
			UpdatedAt: now,
			Usage: tokenUsage{
				TotalTokens:  100,
				InputTokens:  80,
				OutputTokens: 20,
				UsageEvents:  2,
			},
		},
		{
			SessionID: "2",
			UpdatedAt: now.Add(time.Minute),
			Usage: tokenUsage{
				TotalTokens:  40,
				InputTokens:  30,
				OutputTokens: 10,
				UsageEvents:  1,
			},
		},
	}

	repos, totals := summarizeSessionReport(sessions)
	if totals.SessionCount != 2 || totals.RepoCount != 3 || totals.TotalTokens != 140 || totals.UsageEvents != 3 {
		t.Fatalf("totals = %+v", totals)
	}
	want := map[string]int64{
		"nelsong6/tank-operator": 100,
		"nelsong6/glimmung":      100,
		sessionReportUnassigned:  40,
	}
	for _, repo := range repos {
		if got, ok := want[repo.Repo]; !ok || repo.TotalTokens != got {
			t.Fatalf("repo summary %+v not in want %v", repo, want)
		}
		delete(want, repo.Repo)
	}
	if len(want) != 0 {
		t.Fatalf("missing repo summaries: %v", want)
	}
}
