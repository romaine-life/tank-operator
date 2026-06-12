package sessionbus

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// SweepOrphanConsumers walks every JetStream consumer on this bus's
// stream and deletes the ones whose decoded session_id is not in
// liveSessionIDs. Persister consumers and consumers belonging to a
// different orchestrator's scope are skipped.
//
// Why this exists: every (session, provider) pair owns two durable
// consumers — a data-plane consumer (max_ack_pending=1) and a
// control-plane consumer (max_ack_pending=16). The runner-side
// ensureConsumer / ensureControlConsumer in runner-shared/sessionBus.js
// only CREATES these consumers; nothing deletes them when a session
// goes away. Over the lifetime of the JetStream pod the cohort of
// stranded consumers accumulates without bound and eats into the
// JetStream RAM budget (the chart caps each replica at 256 MiB).
// Observed on 2026-05-25: 725 consumers for 6 live sessions, ~50 % of
// the JetStream memory cap. The migration audit checklist in
// CLAUDE.md names this exact failure mode ("For wire-format changes
// affecting durable JetStream consumers, confirm the cutover includes
// an explicit remediation for existing consumers — a deploy alone
// cannot repair them"). This sweep is the durable remediation: it
// runs at orchestrator startup and on a periodic loop so a
// post-deploy backlog clears on its own.
//
// Safety: two complementary guards on two different clocks.
//
//   - MinAge (consumer-creation clock, here): consumers younger than
//     MinAge are kept regardless, so a consumer created before its
//     session row is readable by the sweep (the create-time race) is
//     never deleted.
//   - Liveness recency union (sessions.updated_at clock, in the
//     LiveSessionIDs source): sessions rows are soft-deleted — deletion
//     flips visible=false and bumps updated_at, it never removes the
//     row — so the production live set is
//     sessionregistry.ListLiveIDsForScope: visible sessions UNION
//     sessions updated within the recency window (24 h). The union
//     keeps a just-deleted session's consumers alive while its runner
//     drains in-flight commands; MinAge cannot cover that race because
//     a months-old consumer passes the age check the instant its row
//     goes invisible. (The retired row-exists predicate made the sweep
//     blind: every id ever created counted live, so nothing was ever
//     an orphan.)
//
// The orchestrator's own scope_token is the only other filter —
// cross-scope orchestrators (prod + a test slot sharing one NATS) each
// sweep only their own consumers.

// SweepConfig configures one sweep pass. LiveSessionIDs is the
// authoritative set of session_ids in this scope that own legitimate
// consumers; anything not in this set is deletion-eligible (subject
// to MinAge).
type SweepConfig struct {
	// LiveSessionIDs holds the session ids whose consumers must
	// survive the pass. Production wiring builds it from
	// sessionregistry.ListLiveIDsForScope (visible sessions plus
	// recently-updated soft-deleted sessions) — NOT from bare row
	// existence, which can never shrink because sessions rows are
	// never hard-deleted.
	LiveSessionIDs map[string]struct{}
	// MinAge caps how recent a consumer can be before the sweep
	// considers it. Defaults to 15 minutes; tests can dial down.
	// Guards the create race only — see the package comment above
	// for why the delete race needs the liveness recency union.
	MinAge time.Duration
	// Now returns wall time for the age check. Hook for tests.
	Now func() time.Time
}

// SweepResult summarizes one pass for telemetry + logging.
type SweepResult struct {
	Scanned           int
	SkippedOutOfScope int
	SkippedLive       int
	SkippedTooYoung   int
	Orphans           int
	Deleted           int
	Errors            int
}

// SweepMetrics receives per-pass counts. Wired to Prometheus at the
// orchestrator's observability layer; the interface here keeps this
// package from importing prometheus directly.
type SweepMetrics interface {
	RecordSweepPass(result SweepResult)
}

// ConsumerSweepSource is the small JetStream surface the sweep
// depends on. Defined here (and satisfied by the *Bus.jetstream
// adapter below) so unit tests can supply an in-memory fake instead
// of spinning up an embedded NATS.
type ConsumerSweepSource interface {
	ListConsumers(ctx context.Context) ([]*jetstream.ConsumerInfo, error)
	DeleteConsumer(ctx context.Context, name string) error
}

// SweepOrphanConsumers runs one sweep pass against this bus's NATS
// JetStream stream. Individual delete failures are logged + counted
// in result.Errors but do not abort the pass — the next periodic run
// will retry.
func (b *Bus) SweepOrphanConsumers(ctx context.Context, cfg SweepConfig) (SweepResult, error) {
	if b == nil || b.js == nil {
		return SweepResult{}, errors.New("session bus unavailable")
	}
	// Both streams carry per-session durable consumers now: runner command
	// consumers live on the WorkQueue command stream (issue #1076 item 2);
	// the legacy stream keeps pre-split pods' consumers (and the
	// persister's, which the sweep's name filter already exempts). Sweeping
	// only one would leave the other accumulating against the 256Mi memory
	// JetStream — the 725-consumer incident shape with an alert blind.
	source := &busConsumerSweepSource{js: b.js, stream: b.stream}
	result, err := RunConsumerSweep(ctx, source, b.scope, cfg)
	if err != nil {
		return result, err
	}
	commandSource := &busConsumerSweepSource{js: b.js, stream: b.commandStream}
	commandResult, err := RunConsumerSweep(ctx, commandSource, b.scope, cfg)
	result.Scanned += commandResult.Scanned
	result.SkippedOutOfScope += commandResult.SkippedOutOfScope
	result.SkippedLive += commandResult.SkippedLive
	result.SkippedTooYoung += commandResult.SkippedTooYoung
	result.Orphans += commandResult.Orphans
	result.Deleted += commandResult.Deleted
	result.Errors += commandResult.Errors
	return result, err
}

// RunConsumerSweep is the pure-logic core of the sweep, taking an
// abstract ConsumerSweepSource so unit tests can drive it without
// touching JetStream. The Bus method above is the production wiring.
func RunConsumerSweep(ctx context.Context, source ConsumerSweepSource, scope string, cfg SweepConfig) (SweepResult, error) {
	if source == nil {
		return SweepResult{}, errors.New("consumer sweep source is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	minAge := cfg.MinAge
	if minAge <= 0 {
		minAge = 15 * time.Minute
	}
	myScopeToken := ScopeToken(scope)

	infos, err := source.ListConsumers(ctx)
	if err != nil {
		return SweepResult{}, fmt.Errorf("list consumers: %w", err)
	}

	var result SweepResult
	for _, info := range infos {
		if info == nil {
			continue
		}
		result.Scanned++
		sessionID, ok := DecodeConsumerSessionID(info.Name, myScopeToken)
		if !ok {
			result.SkippedOutOfScope++
			continue
		}
		if _, alive := cfg.LiveSessionIDs[sessionID]; alive {
			result.SkippedLive++
			continue
		}
		age := cfg.Now().Sub(info.Created)
		if age < minAge {
			result.SkippedTooYoung++
			continue
		}
		result.Orphans++
		if err := source.DeleteConsumer(ctx, info.Name); err != nil {
			slog.Warn("orphan consumer delete failed",
				"consumer", info.Name,
				"session_id", sessionID,
				"scope", scope,
				"age_seconds", age.Seconds(),
				"error", err,
			)
			result.Errors++
			continue
		}
		slog.Info("orphan consumer deleted",
			"consumer", info.Name,
			"session_id", sessionID,
			"scope", scope,
			"age_seconds", age.Seconds(),
		)
		result.Deleted++
	}
	return result, nil
}

// DecodeConsumerSessionID extracts the public session_id from a
// runner-side consumer name produced by runner-shared/sessionBus.js:
//
//	consumerName()         -> <provider>_<scopeToken>_<sessionIDToken>
//	controlConsumerName()  -> <provider>_control_<scopeToken>_<sessionIDToken>
//
// Returns ok=false for any name that doesn't match this scope — the
// persister consumer `tank-session-event-persister-<scopeToken>` uses
// `-` separators and falls into this branch, as does any consumer from
// a different orchestrator scope. strings.LastIndex on the scope-token
// needle handles base64-url scope tokens that may themselves contain
// `_`.
func DecodeConsumerSessionID(name, myScopeToken string) (string, bool) {
	if name == "" || myScopeToken == "" {
		return "", false
	}
	needle := "_" + myScopeToken + "_"
	idx := strings.LastIndex(name, needle)
	if idx < 0 {
		return "", false
	}
	prefix := name[:idx]
	suffix := name[idx+len(needle):]
	if prefix == "" || suffix == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(suffix)
	if err != nil {
		return "", false
	}
	sessionID := strings.TrimSpace(string(raw))
	if sessionID == "" {
		return "", false
	}
	return sessionID, true
}

// busConsumerSweepSource adapts the live JetStream calls to the small
// ConsumerSweepSource interface RunConsumerSweep depends on. The Bus's
// js + stream are captured at sweep time.
type busConsumerSweepSource struct {
	js     jetstream.JetStream
	stream string
}

func (s *busConsumerSweepSource) ListConsumers(ctx context.Context) ([]*jetstream.ConsumerInfo, error) {
	stream, err := s.js.Stream(ctx, s.stream)
	if err != nil {
		return nil, fmt.Errorf("get stream %q: %w", s.stream, err)
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lister := stream.ListConsumers(listCtx)
	var out []*jetstream.ConsumerInfo
	for info := range lister.Info() {
		out = append(out, info)
	}
	if err := lister.Err(); err != nil {
		return out, fmt.Errorf("list consumers: %w", err)
	}
	return out, nil
}

func (s *busConsumerSweepSource) DeleteConsumer(ctx context.Context, name string) error {
	return s.js.DeleteConsumer(ctx, s.stream, name)
}
