package main

// Orchestration reconcile backstop. The merged-PR webhook is the fast path that
// walks the DAG, but webhooks are at-least-once and lossy: a dropped delivery
// would otherwise hang a run forever (the same lost-wake failure class the
// rollout work guards against, one layer up). This loop re-drives every
// non-terminal run on an interval — repairing a phase whose PR actually merged
// but whose advance never landed, dispatching a ready/pending phase that should
// have a spoke but doesn't, and bootstrapping a freshly-approved run's root
// phases. Per-replica idempotent: every effect is a guarded conditional write,
// so a double pass (two replicas, or the loop racing a webhook) is harmless.

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const orchestrationReconcileInterval = 2 * time.Minute

func runOrchestrationReconcileLoop(ctx context.Context, engine *orchestrationEngine, interval time.Duration) error {
	if engine == nil {
		return nil
	}
	if interval <= 0 {
		interval = orchestrationReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := engine.reconcileAllActive(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("orchestration reconcile loop pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
