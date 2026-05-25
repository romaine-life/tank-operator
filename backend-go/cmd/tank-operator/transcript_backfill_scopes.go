package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type transcriptBackfillScope struct {
	scope        string
	materializer transcriptRowsMaterializer
}

func transcriptBackfillScopes(pool *pgxpool.Pool, localScope string, local transcriptRowsMaterializer) []transcriptBackfillScope {
	scopeNames := transcriptBackfillScopeNames(localScope, pool != nil)
	out := []transcriptBackfillScope{{
		scope:        scopeNames[0],
		materializer: local,
	}}
	if len(scopeNames) == 1 {
		return out
	}
	out = append(out, transcriptBackfillScope{
		scope: prodSessionScope,
		materializer: transcriptRowsMaterializer{
			events: store.NewPostgresSessionEventStore(pool, prodSessionScope),
			rows:   store.NewPostgresSessionTranscriptRowStore(pool, prodSessionScope),
		},
	})
	return out
}

func transcriptBackfillScopeNames(localScope string, includeReadableProd bool) []string {
	localScope = normalizeSessionScope(localScope)
	if !includeReadableProd || strings.EqualFold(localScope, prodSessionScope) {
		return []string{localScope}
	}
	return []string{localScope, prodSessionScope}
}

func startTranscriptRowBackfills(parent context.Context, backfills []transcriptBackfillScope) {
	for _, backfill := range backfills {
		backfill := backfill
		go func() {
			ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
			defer cancel()
			started := time.Now()
			slog.Info("transcript row backfill started", "session_scope", backfill.scope)
			if err := backfill.materializer.Backfill(ctx); err != nil {
				slog.Error("transcript row backfill failed", "session_scope", backfill.scope, "duration_ms", time.Since(started).Milliseconds(), "error", err)
				return
			}
			slog.Info("transcript row backfill completed", "session_scope", backfill.scope, "duration_ms", time.Since(started).Milliseconds())
		}()
	}
}
