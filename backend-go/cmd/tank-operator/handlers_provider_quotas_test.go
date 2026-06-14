package main

import "testing"

func TestProviderUsageEvidenceNormalizesClaudeOAuthUsage(t *testing.T) {
	raw := map[string]any{
		"five_hour": map[string]any{
			"utilization": 37.0,
			"resets_at":   "2026-06-05T10:00:00Z",
		},
		"seven_day": map[string]any{
			"utilization": 64.0,
			"resets_at":   "2026-06-08T10:00:00Z",
		},
		"seven_day_opus": map[string]any{
			"utilization": 12.0,
			"resets_at":   "2026-06-08T10:00:00Z",
		},
	}

	got := providerUsageEvidence("anthropic", "2026-06-05T07:00:00Z", raw)

	if len(got) != 3 {
		t.Fatalf("evidence len = %d, want 3: %#v", len(got), got)
	}
	assertEvidence(t, got[0], "anthropic", "five_hour", 37.0)
	assertEvidence(t, got[1], "anthropic", "weekly", 64.0)
	assertEvidence(t, got[2], "anthropic", "opus_weekly", 12.0)
}

func TestProviderUsageEvidenceKeepsHighestDuplicateWeeklyUtilization(t *testing.T) {
	raw := map[string]any{
		"rate_limits": []any{
			map[string]any{"name": "weekly", "used_percent": 40.0},
			map[string]any{"name": "seven_day_sonnet", "used_percent": 55.0},
		},
	}

	got := providerUsageEvidence("codex", "2026-06-05T07:00:00Z", raw)

	if len(got) != 1 {
		t.Fatalf("evidence len = %d, want 1: %#v", len(got), got)
	}
	assertEvidence(t, got[0], "codex", "weekly", 55.0)
}

func TestProviderUsageEvidenceNormalizesCodexUsagePayload(t *testing.T) {
	raw := map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent": 68.0,
				"reset_at":     1780648276.0,
			},
			"secondary_window": map[string]any{
				"used_percent": 33.0,
				"reset_at":     1781138255.0,
			},
		},
	}

	got := providerUsageEvidence("codex", "2026-06-05T07:00:00Z", raw)

	if len(got) != 2 {
		t.Fatalf("evidence len = %d, want 2: %#v", len(got), got)
	}
	assertEvidence(t, got[0], "codex", "five_hour", 68.0)
	assertEvidence(t, got[1], "codex", "weekly", 33.0)
}

func TestProviderUsageNamespaceTrimsSessionNamespace(t *testing.T) {
	if got, want := providerUsageNamespace("tank-operator-slot-3-sessions"), "tank-operator-slot-3"; got != want {
		t.Fatalf("providerUsageNamespace = %q, want %q", got, want)
	}
	if got, want := providerUsageNamespace("tank-operator-sessions"), "tank-operator"; got != want {
		t.Fatalf("providerUsageNamespace = %q, want %q", got, want)
	}
	if got, want := providerUsageNamespace("custom"), "custom"; got != want {
		t.Fatalf("providerUsageNamespace = %q, want %q", got, want)
	}
}

func TestDefaultProviderUsageURLIncludesClaudeSecondaryProxy(t *testing.T) {
	s := &appServer{namespace: "tank-operator-slot-7-sessions"}
	got := s.defaultProviderUsageURL("claude_secondary")
	want := "http://claude-secondary-api-proxy.tank-operator-slot-7.svc.cluster.local:9100/usage/claude"
	if got != want {
		t.Fatalf("defaultProviderUsageURL = %q, want %q", got, want)
	}
}

func assertEvidence(t *testing.T, got map[string]any, provider, rateLimitType string, utilization float64) {
	t.Helper()
	if got["provider"] != provider {
		t.Fatalf("provider = %#v, want %q in %#v", got["provider"], provider, got)
	}
	if got["rateLimitType"] != rateLimitType {
		t.Fatalf("rateLimitType = %#v, want %q in %#v", got["rateLimitType"], rateLimitType, got)
	}
	if got["utilization"] != utilization {
		t.Fatalf("utilization = %#v, want %#v in %#v", got["utilization"], utilization, got)
	}
}
