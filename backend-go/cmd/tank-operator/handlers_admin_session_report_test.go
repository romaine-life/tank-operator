package main

import (
	"testing"
	"time"
)

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
