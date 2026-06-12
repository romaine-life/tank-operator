package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// asyncTranscriptRefresher moves the backend-direct write path's
// transcript-row projection off the HTTP request. Before this, every
// persistBackendEvent ran a full RefreshEvent inline — on a
// background-task-heavy session that is a whole-session re-projection inside
// the request, which is how Stop on session 815 exceeded 240s during the
// 2026-06-11 incident recovery ("persist interrupt request: context
// canceled", tank-operator#1051 finding 3). The bus-persister path has always
// refreshed asynchronously relative to its producers; this unifies the
// backend-direct path with that shape.
//
// Semantics preserved:
//   - The durable contract is the session_events row, written synchronously
//     before the handler responds — only the derived projection moves.
//   - Refresh-then-wake ordering: the per-session SSE wake fires after the
//     projection refresh completes, exactly as the bus persister does, so a
//     woken reader always sees the refreshed rows.
//   - Per-session serial workers with batch coalescing reuse the
//     materializer's RefreshEventBatch classification — N queued events for
//     one session cost one projection pass.
//   - A refresh failure is logged and counted; the rows converge via the
//     on-read EnsureSession resync, the documented projection-staleness
//     recovery. The ledger row is already durable either way.
type asyncTranscriptRefresher struct {
	ctx          context.Context
	materializer transcriptRowsMaterializer
	wake         func(storageKey string)

	mu     sync.Mutex
	queues map[string][]asyncRefreshItem
	active map[string]bool
}

type asyncRefreshItem struct {
	event map[string]any
}

func newAsyncTranscriptRefresher(ctx context.Context, materializer transcriptRowsMaterializer, wake func(storageKey string)) *asyncTranscriptRefresher {
	return &asyncTranscriptRefresher{
		ctx:          ctx,
		materializer: materializer,
		wake:         wake,
		queues:       map[string][]asyncRefreshItem{},
		active:       map[string]bool{},
	}
}

// enqueue routes one just-persisted backend event to its session's serial
// refresh queue, spawning a worker if none is running.
func (r *asyncTranscriptRefresher) enqueue(storageKey string, event map[string]any) {
	if r == nil || event == nil {
		return
	}
	r.mu.Lock()
	r.queues[storageKey] = append(r.queues[storageKey], asyncRefreshItem{event: event})
	startWorker := !r.active[storageKey]
	if startWorker {
		r.active[storageKey] = true
	}
	r.mu.Unlock()
	if startWorker {
		go r.runWorker(storageKey)
	}
}

func (r *asyncTranscriptRefresher) runWorker(storageKey string) {
	for {
		r.mu.Lock()
		queue := r.queues[storageKey]
		if len(queue) == 0 || r.ctx.Err() != nil {
			r.active[storageKey] = false
			delete(r.queues, storageKey)
			r.mu.Unlock()
			return
		}
		r.queues[storageKey] = nil
		r.mu.Unlock()

		events := make([]map[string]any, len(queue))
		for i, item := range queue {
			events[i] = item.event
		}
		started := time.Now()
		if err := r.materializer.RefreshEventBatch(r.ctx, events); err != nil {
			recordTranscriptRowMaterialization("backend_async", transcriptRowMaterializationFailureResult(r.ctx, err), time.Since(started))
			slog.Warn("backend async transcript refresh failed; rows converge via on-read resync",
				"storage_key", storageKey,
				"events", len(events),
				"error", err,
			)
		} else {
			recordTranscriptRowMaterialization("backend_async", "refreshed", time.Since(started))
		}
		// Refresh-then-wake, success or not: on failure the woken reader's
		// staleness check triggers the on-read resync sooner rather than
		// waiting out the SSE heartbeat.
		if r.wake != nil && storageKey != "" {
			r.wake(storageKey)
		}
	}
}
