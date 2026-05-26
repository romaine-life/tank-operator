package sessionbus

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func TestDecodeConsumerSessionID(t *testing.T) {
	defaultScope := ScopeToken("default")
	otherScope := ScopeToken("test-slot-7")
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}

	cases := []struct {
		name           string
		consumerName   string
		myScopeToken   string
		wantSessionID  string
		wantOK         bool
	}{
		{
			name:          "data-plane consumer in our scope decodes session id",
			consumerName:  "claude_" + defaultScope + "_" + enc("216"),
			myScopeToken:  defaultScope,
			wantSessionID: "216",
			wantOK:        true,
		},
		{
			name:          "control-plane consumer in our scope decodes session id",
			consumerName:  "claude_control_" + defaultScope + "_" + enc("216"),
			myScopeToken:  defaultScope,
			wantSessionID: "216",
			wantOK:        true,
		},
		{
			name:          "codex provider decodes the same way",
			consumerName:  "codex_" + defaultScope + "_" + enc("232"),
			myScopeToken:  defaultScope,
			wantSessionID: "232",
			wantOK:        true,
		},
		{
			name:         "consumer in a different scope is skipped",
			consumerName: "claude_" + otherScope + "_" + enc("99"),
			myScopeToken: defaultScope,
			wantOK:       false,
		},
		{
			name:         "persister consumer name is skipped (uses dashes, not underscores)",
			consumerName: "tank-session-event-persister-" + defaultScope,
			myScopeToken: defaultScope,
			wantOK:       false,
		},
		{
			name:         "empty name is rejected",
			consumerName: "",
			myScopeToken: defaultScope,
			wantOK:       false,
		},
		{
			name:         "name with no scope-token substring is rejected",
			consumerName: "claude_garbage_token",
			myScopeToken: defaultScope,
			wantOK:       false,
		},
		{
			name:         "trailing token that isn't valid base64 is rejected",
			consumerName: "claude_" + defaultScope + "_!!!notbase64",
			myScopeToken: defaultScope,
			wantOK:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOK := DecodeConsumerSessionID(tc.consumerName, tc.myScopeToken)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v (id=%q)", gotOK, tc.wantOK, gotID)
			}
			if gotOK && gotID != tc.wantSessionID {
				t.Fatalf("session_id = %q, want %q", gotID, tc.wantSessionID)
			}
		})
	}
}

// fakeSweepSource is the in-memory ConsumerSweepSource used by the
// sweep unit tests. Capture-by-construction lets us assert exactly
// which consumers the sweep tried to delete.
type fakeSweepSource struct {
	consumers   []*jetstream.ConsumerInfo
	deleted     []string
	deleteErrFn func(name string) error
	listErr     error
}

func (f *fakeSweepSource) ListConsumers(_ context.Context) ([]*jetstream.ConsumerInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.consumers, nil
}

func (f *fakeSweepSource) DeleteConsumer(_ context.Context, name string) error {
	if f.deleteErrFn != nil {
		if err := f.deleteErrFn(name); err != nil {
			return err
		}
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func mkConsumer(name string, ageMinutes int, now time.Time) *jetstream.ConsumerInfo {
	return &jetstream.ConsumerInfo{
		Name:    name,
		Created: now.Add(-time.Duration(ageMinutes) * time.Minute),
	}
}

func TestRunConsumerSweepDeletesOrphansOldEnough(t *testing.T) {
	scope := "default"
	scopeToken := ScopeToken(scope)
	now := time.Date(2026, 5, 26, 6, 0, 0, 0, time.UTC)
	live216 := base64.RawURLEncoding.EncodeToString([]byte("216"))
	dead404 := base64.RawURLEncoding.EncodeToString([]byte("404"))
	dead505 := base64.RawURLEncoding.EncodeToString([]byte("505"))

	source := &fakeSweepSource{
		consumers: []*jetstream.ConsumerInfo{
			mkConsumer("claude_"+scopeToken+"_"+live216, 60, now),            // live, skip
			mkConsumer("claude_control_"+scopeToken+"_"+live216, 60, now),    // live, skip
			mkConsumer("claude_"+scopeToken+"_"+dead404, 60, now),            // orphan, delete
			mkConsumer("claude_control_"+scopeToken+"_"+dead404, 60, now),    // orphan, delete
			mkConsumer("codex_"+scopeToken+"_"+dead505, 60, now),             // orphan, delete
			mkConsumer("claude_"+scopeToken+"_"+dead404, 1, now),             // too-young duplicate, skip
			mkConsumer("tank-session-event-persister-"+scopeToken, 999, now), // persister, skip
			mkConsumer("claude_"+ScopeToken("test-slot-3")+"_"+dead404, 60, now), // other scope, skip
		},
	}

	result, err := RunConsumerSweep(
		context.Background(),
		source,
		scope,
		SweepConfig{
			LiveSessionIDs: map[string]struct{}{"216": {}},
			MinAge:         15 * time.Minute,
			Now:            func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Scanned != 8 {
		t.Errorf("Scanned = %d, want 8", result.Scanned)
	}
	if result.SkippedLive != 2 {
		t.Errorf("SkippedLive = %d, want 2", result.SkippedLive)
	}
	if result.SkippedOutOfScope != 2 {
		t.Errorf("SkippedOutOfScope = %d, want 2 (persister + other-scope)", result.SkippedOutOfScope)
	}
	if result.SkippedTooYoung != 1 {
		t.Errorf("SkippedTooYoung = %d, want 1", result.SkippedTooYoung)
	}
	if result.Orphans != 3 {
		t.Errorf("Orphans = %d, want 3", result.Orphans)
	}
	if result.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3", result.Deleted)
	}
	if result.Errors != 0 {
		t.Errorf("Errors = %d, want 0", result.Errors)
	}

	// Pin the exact deletion set so we don't accidentally widen the
	// blast radius in a future change.
	wantDeleted := map[string]bool{
		"claude_" + scopeToken + "_" + dead404:         true,
		"claude_control_" + scopeToken + "_" + dead404: true,
		"codex_" + scopeToken + "_" + dead505:          true,
	}
	for _, name := range source.deleted {
		if !wantDeleted[name] {
			t.Errorf("unexpected delete of %q", name)
		}
		delete(wantDeleted, name)
	}
	for missing := range wantDeleted {
		t.Errorf("expected delete of %q did not happen", missing)
	}
}

func TestRunConsumerSweepCountsDeleteFailuresWithoutAborting(t *testing.T) {
	scope := "default"
	scopeToken := ScopeToken(scope)
	now := time.Date(2026, 5, 26, 6, 0, 0, 0, time.UTC)

	source := &fakeSweepSource{
		consumers: []*jetstream.ConsumerInfo{
			mkConsumer("claude_"+scopeToken+"_"+base64.RawURLEncoding.EncodeToString([]byte("100")), 60, now),
			mkConsumer("claude_"+scopeToken+"_"+base64.RawURLEncoding.EncodeToString([]byte("200")), 60, now),
			mkConsumer("claude_"+scopeToken+"_"+base64.RawURLEncoding.EncodeToString([]byte("300")), 60, now),
		},
		deleteErrFn: func(name string) error {
			if strings.HasSuffix(name, base64.RawURLEncoding.EncodeToString([]byte("200"))) {
				return errors.New("transient NATS error")
			}
			return nil
		},
	}

	result, err := RunConsumerSweep(
		context.Background(),
		source,
		scope,
		SweepConfig{
			LiveSessionIDs: map[string]struct{}{},
			MinAge:         15 * time.Minute,
			Now:            func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Orphans != 3 {
		t.Errorf("Orphans = %d, want 3", result.Orphans)
	}
	if result.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2 (the third should be counted as an error, not aborted)", result.Deleted)
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d, want 1", result.Errors)
	}
}

func TestRunConsumerSweepRequiresSource(t *testing.T) {
	_, err := RunConsumerSweep(context.Background(), nil, "default", SweepConfig{})
	if err == nil {
		t.Fatal("expected error when source is nil")
	}
}

func TestRunConsumerSweepUsesDefaultMinAgeWhenZero(t *testing.T) {
	scope := "default"
	scopeToken := ScopeToken(scope)
	now := time.Date(2026, 5, 26, 6, 0, 0, 0, time.UTC)
	session100 := base64.RawURLEncoding.EncodeToString([]byte("100"))

	source := &fakeSweepSource{
		consumers: []*jetstream.ConsumerInfo{
			// 10 minutes old; under the default 15m floor.
			mkConsumer("claude_"+scopeToken+"_"+session100, 10, now),
		},
	}

	result, err := RunConsumerSweep(
		context.Background(),
		source,
		scope,
		SweepConfig{
			LiveSessionIDs: map[string]struct{}{},
			// MinAge intentionally left zero
			Now: func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.SkippedTooYoung != 1 {
		t.Fatalf("SkippedTooYoung = %d, want 1 (default MinAge should be 15m)", result.SkippedTooYoung)
	}
	if result.Deleted != 0 {
		t.Fatalf("Deleted = %d, want 0", result.Deleted)
	}
}
