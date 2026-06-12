package pgstore

// Integration coverage for the stranded-turn sweep's AskUserQuestion
// pause-linkage exclusion (the 2026-06-12 first-day incident: the sweep
// wrote false turn.command_failed terminals onto 54% of its output —
// question shells, asking turns paused on the user, and answered asking
// turns whose terminal lives under the rotated continuation id) and for
// the fleet-wide pipeline-liveness probe that gates the sweep during
// persister outages. Runs against a throwaway schema with the real
// migrations so the SQL predicate and the session_events_input_pause
// partial index are exercised exactly as production runs them.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func newStrandedTurnTestPool(t *testing.T, ctx context.Context, label string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_%s_%d", label, time.Now().UnixNano())
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE")
		adminPool.Close()
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test dsn: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pool
}

// backdateSeededEvents copies each seeded event's payload created_at into
// the created_at column. The Upsert INSERT leaves the column at its now()
// default (the sweep windows deliberately ride persist-time, not producer
// time), so tests that exercise the time-window predicates must backdate
// the column to the seeded timeline after seeding.
func backdateSeededEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE session_events
		SET created_at = (payload ->> 'created_at')::timestamptz
		WHERE payload ? 'created_at'
	`); err != nil {
		t.Fatalf("backdate seeded events: %v", err)
	}
}

// seedEvent stamps deterministic timing onto an event map and upserts it.
// The order_key mirrors the producers' wall-clock + counter + id shape so
// per-session ordering in the test matches production rows.
func seedEvent(t *testing.T, ctx context.Context, st store.SessionEventStore, event map[string]any, at time.Time, seq int) {
	t.Helper()
	event["created_at"] = at.UTC().Format(time.RFC3339Nano)
	event["written_at"] = at.UTC().Format(time.RFC3339Nano)
	eventID, _ := event["event_id"].(string)
	event["order_key"] = fmt.Sprintf("%013d-%08d-%s", at.UnixMilli(), seq, eventID)
	if _, err := st.Upsert(ctx, event); err != nil {
		t.Fatalf("upsert %v: %v", event["event_id"], err)
	}
}

// runnerTurnEvent builds a minimal valid runner-actor lifecycle event
// (turn.claimed / turn.started and friends carry no payload contract).
func runnerTurnEvent(sessionID, storageKey, turnID, eventType string) map[string]any {
	return map[string]any{
		"event_id":        turnID + ":" + eventType,
		"conversation_id": sessionID,
		"session_id":      sessionID,
		"tank_session_id": storageKey,
		"turn_id":         turnID,
		"actor":           "runner",
		"source":          "claude",
		"type":            eventType,
		"visibility":      "durable",
		"payload":         map[string]any{},
	}
}

// questionShellSubmitted mirrors runner-shared/conversation-builders.js
// askUserQuestionHandoffEvents().questionSubmitted: the synthetic
// turn.submitted that anchors the question as a user-facing turn. It is
// never claimed and never receives a terminal — by design.
func questionShellSubmitted(sessionID, storageKey, questionTurnID, questionNonce string) map[string]any {
	return map[string]any{
		"event_id":        questionTurnID + ":turn.submitted",
		"conversation_id": sessionID,
		"session_id":      sessionID,
		"tank_session_id": storageKey,
		"turn_id":         questionTurnID,
		"client_nonce":    questionNonce,
		"actor":           "runner",
		"source":          "tank",
		"type":            "turn.submitted",
		"visibility":      "durable",
		"producer":        map[string]any{"name": "tank-operator", "runtime": "claude"},
		"payload":         map[string]any{"status": "submitted"},
	}
}

// awaitingInputEvent mirrors the runner's turn.awaiting_input: it rides the
// synthetic question turn id and links the asking turn through the payload.
func awaitingInputEvent(sessionID, storageKey, askingTurnID, questionTurnID string) map[string]any {
	ev := runnerTurnEvent(sessionID, storageKey, questionTurnID, "turn.awaiting_input")
	ev["event_id"] = questionTurnID + ":turn.awaiting_input:runner"
	ev["payload"] = map[string]any{
		"asking_turn_id":   askingTurnID,
		"question_turn_id": questionTurnID,
		"questions":        []any{map[string]any{"question": "Proceed?"}},
	}
	return ev
}

// seedUserTurn writes the backend boundary pair (user_message.created +
// turn.submitted) for a user-submitted turn and returns its turn id.
func seedUserTurn(t *testing.T, ctx context.Context, st store.SessionEventStore, sessionID, storageKey, nonce, text string, at time.Time, seq int) string {
	t.Helper()
	_, events, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             "user@example.com",
		ClientNonce:       nonce,
		Text:              text,
		Message:           map[string]any{"role": "user", "content": text},
		Runtime:           "claude",
		Now:               at,
	})
	if err != nil {
		t.Fatalf("build user turn %s: %v", nonce, err)
	}
	for i, event := range events {
		seedEvent(t, ctx, st, event, at.Add(time.Duration(i)*time.Millisecond), seq+i)
	}
	return conversation.TurnIDForClientNonce(nonce)
}

func TestFindStrandedTurnsExcludesAskUserQuestionShapes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "stranded_pause")

	scope := "default"
	eventStore := store.NewPostgresSessionEventStore(pool, scope)
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-3 * time.Hour)

	storage := func(sessionID string) string {
		return sessionmodel.SessionStorageKey(scope, sessionID)
	}

	// S1: genuine never-claimed strand — the command-lost class.
	s1Turn := seedUserTurn(t, ctx, eventStore, "s1", storage("s1"), "s1-turn", "hello", old, 0)

	// S2: genuine progressed strand — claimed/started, runner died mid-turn.
	s2Turn := seedUserTurn(t, ctx, eventStore, "s2", storage("s2"), "s2-turn", "work please", old, 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s2", storage("s2"), s2Turn, "turn.claimed"), old.Add(time.Second), 10)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s2", storage("s2"), s2Turn, "turn.started"), old.Add(2*time.Second), 11)

	// S3: live AskUserQuestion — asking turn paused on the user plus the
	// synthetic question shell. Neither may ever be a strand candidate, no
	// matter how old: the question legitimately waits for a human.
	s3Asking := seedUserTurn(t, ctx, eventStore, "s3", storage("s3"), "s3-turn", "do the thing", old, 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s3", storage("s3"), s3Asking, "turn.claimed"), old.Add(time.Second), 10)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s3", storage("s3"), s3Asking, "turn.started"), old.Add(2*time.Second), 11)
	s3QuestionNonce := "question-abc123"
	s3Question := conversation.TurnIDForClientNonce(s3QuestionNonce)
	seedEvent(t, ctx, eventStore, questionShellSubmitted("s3", storage("s3"), s3Question, s3QuestionNonce), old.Add(3*time.Second), 12)
	seedEvent(t, ctx, eventStore, awaitingInputEvent("s3", storage("s3"), s3Asking, s3Question), old.Add(4*time.Second), 13)

	// S4: answered AskUserQuestion whose rotated continuation reached a
	// terminal. The asking turn's closure IS the answer + rotation; it never
	// gets a terminal of its own and must stay excluded forever.
	s4Asking := seedUserTurn(t, ctx, eventStore, "s4", storage("s4"), "s4-turn", "ask me", old.Add(-time.Hour), 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s4", storage("s4"), s4Asking, "turn.claimed"), old.Add(-time.Hour).Add(time.Second), 10)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s4", storage("s4"), s4Asking, "turn.started"), old.Add(-time.Hour).Add(2*time.Second), 11)
	s4QuestionNonce := "question-def456"
	s4Question := conversation.TurnIDForClientNonce(s4QuestionNonce)
	seedEvent(t, ctx, eventStore, questionShellSubmitted("s4", storage("s4"), s4Question, s4QuestionNonce), old.Add(-time.Hour).Add(3*time.Second), 12)
	seedEvent(t, ctx, eventStore, awaitingInputEvent("s4", storage("s4"), s4Asking, s4Question), old.Add(-time.Hour).Add(4*time.Second), 13)
	s4AnswerNonce := "answer-0123456789abcdef01234567"
	seedEvent(t, ctx, eventStore, conversation.TurnInputAnsweredEventMap(conversation.TurnInputAnsweredArgs{
		SessionID:          "s4",
		SessionStorageKey:  storage("s4"),
		Email:              "user@example.com",
		TurnID:             s4Question,
		ClientNonce:        s4AnswerNonce,
		ProviderItemID:     "item-1",
		QuestionTimelineID: "tl-1",
		Answers:            map[string][]string{"Proceed?": {"yes"}},
		Now:                old,
	}), old, 20)
	s4Continuation := seedUserTurn(t, ctx, eventStore, "s4", storage("s4"), s4AnswerNonce, "yes", old.Add(time.Second), 30)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s4", storage("s4"), s4Continuation, "turn.claimed"), old.Add(2*time.Second), 40)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s4", storage("s4"), s4Continuation, "turn.started"), old.Add(3*time.Second), 41)
	seedEvent(t, ctx, eventStore, conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
		SessionID:         "s4",
		SessionStorageKey: storage("s4"),
		Email:             "user@example.com",
		TurnID:            s4Continuation,
		ClientNonce:       s4AnswerNonce,
		Runtime:           "claude",
		Reason:            "test terminal",
		Now:               old.Add(4 * time.Second),
	}), old.Add(4*time.Second), 42)

	// S5: answered AskUserQuestion whose input_reply command was lost — the
	// rotated continuation turn was durably submitted but never claimed.
	// THAT turn is the genuine strand; the asking turn and shell are not.
	s5Asking := seedUserTurn(t, ctx, eventStore, "s5", storage("s5"), "s5-turn", "ask then die", old.Add(-time.Hour), 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s5", storage("s5"), s5Asking, "turn.claimed"), old.Add(-time.Hour).Add(time.Second), 10)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("s5", storage("s5"), s5Asking, "turn.started"), old.Add(-time.Hour).Add(2*time.Second), 11)
	s5QuestionNonce := "question-fed789"
	s5Question := conversation.TurnIDForClientNonce(s5QuestionNonce)
	seedEvent(t, ctx, eventStore, questionShellSubmitted("s5", storage("s5"), s5Question, s5QuestionNonce), old.Add(-time.Hour).Add(3*time.Second), 12)
	seedEvent(t, ctx, eventStore, awaitingInputEvent("s5", storage("s5"), s5Asking, s5Question), old.Add(-time.Hour).Add(4*time.Second), 13)
	s5AnswerNonce := "answer-fedcba9876543210fedcba98"
	seedEvent(t, ctx, eventStore, conversation.TurnInputAnsweredEventMap(conversation.TurnInputAnsweredArgs{
		SessionID:          "s5",
		SessionStorageKey:  storage("s5"),
		Email:              "user@example.com",
		TurnID:             s5Question,
		ClientNonce:        s5AnswerNonce,
		ProviderItemID:     "item-2",
		QuestionTimelineID: "tl-2",
		Answers:            map[string][]string{"Proceed?": {"no"}},
		Now:                old,
	}), old, 20)
	s5Continuation := seedUserTurn(t, ctx, eventStore, "s5", storage("s5"), s5AnswerNonce, "no", old.Add(time.Second), 30)

	backdateSeededEvents(t, ctx, pool)

	rows, err := eventStore.FindStrandedTurns(ctx,
		now.Add(-30*time.Minute),  // olderThan
		now.Add(-30*time.Minute),  // quietSince
		now.Add(-30*24*time.Hour), // notBefore
		50,
	)
	if err != nil {
		t.Fatalf("FindStrandedTurns: %v", err)
	}

	got := map[string]store.StrandedTurn{}
	for _, row := range rows {
		got[row.TankSessionID+"/"+row.TurnID] = row
	}

	want := map[string]bool{
		storage("s1") + "/" + s1Turn:         false, // progressed=false
		storage("s2") + "/" + s2Turn:         true,  // progressed=true
		storage("s5") + "/" + s5Continuation: false, // lost input_reply, never claimed
	}
	if len(got) != len(want) {
		t.Fatalf("stranded rows = %d (%v), want exactly %d genuine strands", len(got), keysOfStranded(got), len(want))
	}
	for key, wantProgressed := range want {
		row, ok := got[key]
		if !ok {
			t.Fatalf("genuine strand %s missing from results: %v", key, keysOfStranded(got))
		}
		if row.Progressed != wantProgressed {
			t.Fatalf("strand %s progressed = %v, want %v", key, row.Progressed, wantProgressed)
		}
	}

	for _, excluded := range []string{
		storage("s3") + "/" + s3Asking,
		storage("s3") + "/" + s3Question,
		storage("s4") + "/" + s4Asking,
		storage("s4") + "/" + s4Question,
		storage("s4") + "/" + s4Continuation,
		storage("s5") + "/" + s5Asking,
		storage("s5") + "/" + s5Question,
	} {
		if _, ok := got[excluded]; ok {
			t.Fatalf("AskUserQuestion-linked turn %s was reported as stranded", excluded)
		}
	}
}

func keysOfStranded(rows map[string]store.StrandedTurn) []string {
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	return keys
}

func TestHasRecentRunnerEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "runner_liveness")

	scope := "default"
	eventStore := store.NewPostgresSessionEventStore(pool, scope)
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-3 * time.Hour)
	storageKey := sessionmodel.SessionStorageKey(scope, "live1")

	// Old runner progress only: outside the window, not proof of life.
	turnID := seedUserTurn(t, ctx, eventStore, "live1", storageKey, "live1-turn", "old work", old, 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("live1", storageKey, turnID, "turn.claimed"), old.Add(time.Second), 10)
	backdateSeededEvents(t, ctx, pool)

	alive, err := eventStore.HasRecentRunnerEvent(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("HasRecentRunnerEvent: %v", err)
	}
	if alive {
		t.Fatalf("alive = true with no runner events in the window")
	}

	// A fresh backend-written boundary (turn.submitted lands over HTTP even
	// during a persister outage) must NOT count as pipeline proof of life.
	seedUserTurn(t, ctx, eventStore, "live2", sessionmodel.SessionStorageKey(scope, "live2"), "live2-turn", "fresh submit", now, 0)
	backdateSeededEvents(t, ctx, pool)
	alive, err = eventStore.HasRecentRunnerEvent(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("HasRecentRunnerEvent after fresh submit: %v", err)
	}
	if alive {
		t.Fatalf("alive = true from a backend-written turn.submitted; backend types must not satisfy the liveness gate")
	}

	// A fresh runner claim is proof.
	freshTurn := conversation.TurnIDForClientNonce("live2-turn")
	seedEvent(t, ctx, eventStore, runnerTurnEvent("live2", sessionmodel.SessionStorageKey(scope, "live2"), freshTurn, "turn.claimed"), now, 20)
	alive, err = eventStore.HasRecentRunnerEvent(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("HasRecentRunnerEvent after fresh claim: %v", err)
	}
	if !alive {
		t.Fatalf("alive = false after a fresh runner turn.claimed in the window")
	}
}

// TestFindStrandedTurnsIsScopeGated pins the blast-radius boundary added
// after issue #1079 item 4 reproduced live (2026-06-12): test-slot
// orchestrators share the production database and run arbitrary branch
// code, so each orchestrator's sweep may only address sessions in its own
// scope. Default-scope storage keys are bare ids; slot scopes own their
// 'scope:' prefix.
func TestFindStrandedTurnsIsScopeGated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "stranded_scope")

	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-3 * time.Hour)

	defaultStore := store.NewPostgresSessionEventStore(pool, "default")
	slotStore := store.NewPostgresSessionEventStore(pool, "slot-1")

	defaultTurn := seedUserTurn(t, ctx, defaultStore, "d1", sessionmodel.SessionStorageKey("default", "d1"), "d1-turn", "prod strand", old, 0)
	slotTurn := seedUserTurn(t, ctx, slotStore, "s1", sessionmodel.SessionStorageKey("slot-1", "s1"), "s1-turn", "slot strand", old, 0)
	backdateSeededEvents(t, ctx, pool)

	window := func(st store.SessionEventStore) map[string]bool {
		t.Helper()
		rows, err := st.FindStrandedTurns(ctx,
			now.Add(-30*time.Minute), now.Add(-30*time.Minute), now.Add(-30*24*time.Hour), 50)
		if err != nil {
			t.Fatalf("FindStrandedTurns: %v", err)
		}
		got := map[string]bool{}
		for _, row := range rows {
			got[row.TankSessionID+"/"+row.TurnID] = true
		}
		return got
	}

	fromDefault := window(defaultStore)
	if !fromDefault[sessionmodel.SessionStorageKey("default", "d1")+"/"+defaultTurn] {
		t.Fatalf("default-scope sweep missed its own strand: %v", fromDefault)
	}
	if fromDefault[sessionmodel.SessionStorageKey("slot-1", "s1")+"/"+slotTurn] {
		t.Fatalf("default-scope sweep crossed into slot scope: %v", fromDefault)
	}

	fromSlot := window(slotStore)
	if !fromSlot[sessionmodel.SessionStorageKey("slot-1", "s1")+"/"+slotTurn] {
		t.Fatalf("slot-scope sweep missed its own strand: %v", fromSlot)
	}
	if fromSlot[sessionmodel.SessionStorageKey("default", "d1")+"/"+defaultTurn] {
		t.Fatalf("slot-scope sweep crossed into default scope: %v", fromSlot)
	}

	// The liveness probe is scope-bounded the same way: fresh runner
	// progress in the slot scope must not register as proof of life for
	// the default scope.
	freshSlotTurn := conversation.TurnIDForClientNonce("s1-live")
	seedEvent(t, ctx, slotStore, runnerTurnEvent("s1", sessionmodel.SessionStorageKey("slot-1", "s1"), freshSlotTurn, "turn.claimed"), now, 40)
	aliveDefault, err := defaultStore.HasRecentRunnerEvent(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("HasRecentRunnerEvent default: %v", err)
	}
	if aliveDefault {
		t.Fatalf("slot-scope runner progress satisfied the default scope's liveness gate")
	}
	aliveSlot, err := slotStore.HasRecentRunnerEvent(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("HasRecentRunnerEvent slot: %v", err)
	}
	if !aliveSlot {
		t.Fatalf("slot scope's own runner progress did not satisfy its liveness gate")
	}
}
