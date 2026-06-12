package main

// Session-row ↔ pod reconciler (issue #1079 item 2). The K8s watch only
// converges state for pods that EXIST: a backend crash between the
// registry row write and the pod create, a pod force-deletion or node GC
// that swallowed the Terminating update, or a missed watch event leaves a
// visible Pending/Active row with no pod behind it — a phantom the
// sidebar shows forever and nothing else repairs (the durable reaper
// requires a settled activity status; a phantom row often has none).
//
// The reconciler is the row-side backstop: stale pod-backed rows (10-min
// floor keeps freshly-created rows whose pods are seconds away out of the
// candidate set) are diffed against the actual cluster pod list; rows
// whose pod is gone get the same PodFailed transition the watch would
// have written, through the same RowWriter — status Failed, sidebar
// publish, durable row. Per-replica and idempotent: a double transition
// is a repeated Failed write and a second publish, both harmless, so no
// leader election (same posture as the idle reaper's conditional claim).
//
// The reverse direction (pod with no row) cannot arise from the create
// path anymore — Manager.Create aborts before the pod exists when the
// registry write fails — and manual pod creation is out of scope.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
)

const (
	sessionRowReconcileInterval  = 5 * time.Minute
	sessionRowReconcileStaleAge  = 10 * time.Minute
	sessionRowReconcileBatchSize = 50
)

// staleRowLister is the registry slice the reconciler needs (interface for
// the cmd-level unit tests around the DSN-gated store).
type staleRowLister interface {
	ListStalePodBackedRows(ctx context.Context, cutoff time.Time, limit int) ([]sessionregistry.StalePodBackedRow, error)
}

// rowTransitionWriter is the RowWriter slice the reconciler needs.
type rowTransitionWriter interface {
	RecordTransition(ctx context.Context, event sessioncontroller.Event) (sessioncontroller.TransitionOutcome, error)
}

func runSessionRowReconcileLoop(ctx context.Context, app *appServer, lister staleRowLister, writer rowTransitionWriter, interval time.Duration) error {
	if app == nil || app.k8s == nil || lister == nil || writer == nil {
		return nil
	}
	if interval <= 0 {
		interval = sessionRowReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.reconcileSessionRows(ctx, lister, writer, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("session row reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) reconcileSessionRows(ctx context.Context, lister staleRowLister, writer rowTransitionWriter, now time.Time) error {
	rows, err := lister.ListStalePodBackedRows(ctx, now.Add(-sessionRowReconcileStaleAge), sessionRowReconcileBatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// One cluster list per pass, not per row. The label selector matches
	// what the session manager stamps on every pod it creates.
	pods, err := s.k8s.CoreV1().Pods(sessionmodel.SessionsNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=tank-operator",
	})
	if err != nil {
		return err
	}
	livePods := make(map[string]struct{}, len(pods.Items))
	for _, pod := range pods.Items {
		livePods[pod.Name] = struct{}{}
	}
	for _, row := range rows {
		if _, ok := livePods[row.PodName]; ok {
			continue
		}
		outcome, err := writer.RecordTransition(ctx, sessioncontroller.Event{
			Email:        row.Email,
			SessionScope: s.sessionScope,
			SessionID:    row.SessionID,
			Type:         sessioncontroller.EventTypePodFailed,
			OccurredAt:   now.Format(time.RFC3339Nano),
			Payload: map[string]any{
				"reason": "session row reconciler: pod missing from cluster",
				"pod":    row.PodName,
			},
		})
		if err != nil {
			recordSessionRowReconciled("transition_failed")
			slog.Warn("session row reconcile: transition failed",
				"session_id", row.SessionID, "owner", row.Email, "pod", row.PodName, "error", err)
			continue
		}
		recordSessionRowReconciled("pod_missing_failed")
		slog.Warn("session row reconciled: pod missing, row marked Failed",
			"session_id", row.SessionID,
			"owner", row.Email,
			"pod", row.PodName,
			"prior_status", row.Status,
			"outcome", string(outcome),
		)
	}
	return nil
}
