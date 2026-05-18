// K8s watch loop for the session controller. Single-writer Kubernetes
// pod watcher that derives pod-state transitions and updates the
// sessions row columns (status / ready_at / terminating_at) through
// RowWriter, then republishes the post-write row on the per-owner
// NATS row-update subject. The orchestrator deployment runs with
// replicas=2 (k8s/values.yaml), so the watch holds a
// coordination.k8s.io/Lease via the standard leaderelection library
// and only the leader writes; the follower keeps a warm K8s client
// and SSE serving stays available everywhere.
//
// History: this code was a separate package until
// docs/session-list-redesign.md Phase 1 consolidated it into
// sessioncontroller alongside chat_activity.go so the three writers
// (user-action, K8s, chat) converge through a single RowWriter. Phase
// 4 dropped the durable session_lifecycle_events ledger entirely —
// re-observation dedup is in-process now (transitionTracker.last);
// the post-restart re-emit pattern is harmless because each emit
// converges the row to observed state and the SPA reconciles by
// primary key.

package sessioncontroller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// K8sWatchMetrics is the optional observability hook the watch loop
// reports transition counts and lag through. Wired to prometheus in
// cmd/tank-operator/observability.go.
type K8sWatchMetrics interface {
	RecordTransition(eventType string)
	RecordLag(seconds float64)
	RecordLeaderStatus(isLeader bool)
}

type noopK8sWatchMetrics struct{}

func (noopK8sWatchMetrics) RecordTransition(_ string) {}
func (noopK8sWatchMetrics) RecordLag(_ float64)       {}
func (noopK8sWatchMetrics) RecordLeaderStatus(_ bool) {}

// K8sWatchConfig wires the watch loop with everything it needs to
// produce durable rows + NATS payloads. Namespace is the sessions
// namespace (`tank-operator-sessions`); LeaseNamespace is where the
// lease lives (orchestrator namespace); Identity is what shows up in
// the Lease's holderIdentity (use the pod name). All durable writes
// go through Writer.RecordTransition so the ledger row, the sessions
// row column update, and the NATS publish are a single call.
type K8sWatchConfig struct {
	K8s            kubernetes.Interface
	Writer         *RowWriter
	Metrics        K8sWatchMetrics
	Scope          string
	Namespace      string // sessions namespace ("tank-operator-sessions")
	LeaseNamespace string // orchestrator namespace
	LeaseName      string // defaults to "tank-operator-session-controller"
	Identity       string // pod name (HOSTNAME env)
	ResyncPeriod   time.Duration
	LeaseDuration  time.Duration
	RenewDeadline  time.Duration
	RetryPeriod    time.Duration
	// SkipLeaderElection runs the watch without a lease — only for
	// single-replica local dev and unit tests. In production the
	// orchestrator runs with replicas=2 and the lease is required.
	SkipLeaderElection bool
}

// RunK8sWatch blocks until ctx is canceled. It manages the lease
// lifecycle internally: while the leader, runs the informer and writes
// lifecycle rows; while the follower, sleeps and re-attempts
// leadership. Single-writer guarantee comes from the lease, not from
// the informer itself — two replicas without a lease would each emit
// duplicate rows on the same transition (the unique constraint would
// catch them, but the publish side would still send two NATS
// messages).
func RunK8sWatch(ctx context.Context, cfg K8sWatchConfig) error {
	cfg = applyK8sWatchDefaults(cfg)
	if cfg.K8s == nil {
		return fmt.Errorf("sessioncontroller k8s-watch: K8s client is required")
	}
	if cfg.Writer == nil {
		return fmt.Errorf("sessioncontroller k8s-watch: RowWriter is required")
	}
	if cfg.SkipLeaderElection {
		return runK8sWatchLeader(ctx, cfg)
	}
	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		cfg.LeaseNamespace, cfg.LeaseName,
		cfg.K8s.CoreV1(),
		cfg.K8s.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: cfg.Identity},
	)
	if err != nil {
		return fmt.Errorf("sessioncontroller k8s-watch: build lease lock: %w", err)
	}
	for ctx.Err() == nil {
		// LeaderElector blocks until lease is lost or ctx is canceled.
		// On loss we re-enter the loop and contend again.
		leaderCfg := leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   cfg.LeaseDuration,
			RenewDeadline:   cfg.RenewDeadline,
			RetryPeriod:     cfg.RetryPeriod,
			ReleaseOnCancel: true,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					cfg.Metrics.RecordLeaderStatus(true)
					slog.Info("sessioncontroller k8s-watch: started leading",
						"identity", cfg.Identity,
						"lease", cfg.LeaseNamespace+"/"+cfg.LeaseName,
					)
					if err := runK8sWatchLeader(leaderCtx, cfg); err != nil {
						slog.Warn("sessioncontroller k8s-watch: leader run failed",
							"error", err,
							"identity", cfg.Identity,
						)
					}
				},
				OnStoppedLeading: func() {
					cfg.Metrics.RecordLeaderStatus(false)
					slog.Info("sessioncontroller k8s-watch: stopped leading",
						"identity", cfg.Identity,
					)
				},
				OnNewLeader: func(holder string) {
					if holder == cfg.Identity {
						return
					}
					slog.Info("sessioncontroller k8s-watch: new leader observed",
						"holder", holder,
						"identity", cfg.Identity,
					)
				},
			},
		}
		elector, err := leaderelection.NewLeaderElector(leaderCfg)
		if err != nil {
			return fmt.Errorf("sessioncontroller k8s-watch: build leader elector: %w", err)
		}
		elector.Run(ctx)
	}
	return ctx.Err()
}

// runK8sWatchLeader runs the actual informer + transition emitter.
// Called from OnStartedLeading (or directly when SkipLeaderElection is
// true).
func runK8sWatchLeader(ctx context.Context, cfg K8sWatchConfig) error {
	tracker := newTransitionTracker(cfg.Writer, cfg.Metrics, cfg.Scope)
	factory := informers.NewSharedInformerFactoryWithOptions(
		cfg.K8s,
		cfg.ResyncPeriod,
		informers.WithNamespace(cfg.Namespace),
	)
	podInformer := factory.Core().V1().Pods().Informer()
	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			tracker.handleUpsert(ctx, nil, pod)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldPod, _ := oldObj.(*corev1.Pod)
			newPod, ok := newObj.(*corev1.Pod)
			if !ok {
				return
			}
			tracker.handleUpsert(ctx, oldPod, newPod)
		},
		DeleteFunc: func(obj any) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				// DeletedFinalStateUnknown wrapper — unwrap.
				tomb, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				pod, ok = tomb.Obj.(*corev1.Pod)
				if !ok {
					return
				}
			}
			tracker.handleDelete(ctx, pod)
		},
	})
	if err != nil {
		return fmt.Errorf("sessioncontroller k8s-watch: register handler: %w", err)
	}
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	<-ctx.Done()
	return ctx.Err()
}

func applyK8sWatchDefaults(cfg K8sWatchConfig) K8sWatchConfig {
	if cfg.Scope == "" {
		cfg.Scope = "default"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = sessionmodel.SessionsNamespace
	}
	if strings.TrimSpace(cfg.LeaseName) == "" {
		cfg.LeaseName = "tank-operator-session-controller"
	}
	if strings.TrimSpace(cfg.Identity) == "" {
		cfg.Identity = strings.TrimSpace(os.Getenv("HOSTNAME"))
		if cfg.Identity == "" {
			cfg.Identity = "tank-operator-session-controller"
		}
	}
	if cfg.ResyncPeriod == 0 {
		cfg.ResyncPeriod = 10 * time.Minute
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 15 * time.Second
	}
	if cfg.RenewDeadline == 0 {
		cfg.RenewDeadline = 10 * time.Second
	}
	if cfg.RetryPeriod == 0 {
		cfg.RetryPeriod = 2 * time.Second
	}
	if cfg.Metrics == nil {
		cfg.Metrics = noopK8sWatchMetrics{}
	}
	return cfg
}

// transitionTracker keeps per-pod last-emitted state so the watch
// emits one row UPDATE per real state change. Restart-safe: on first
// sight of a pod we re-derive the current state and attempt to emit;
// each emit converges the sessions row's column values toward observed
// state, so a duplicate emit lands an idempotent UPDATE (same column
// values, row_version still bumps and clients re-render cleanly).
type transitionTracker struct {
	writer  *RowWriter
	metrics K8sWatchMetrics
	scope   string

	mu   sync.Mutex
	last map[types.UID]podState
}

type podState struct {
	phase        corev1.PodPhase
	ready        bool
	terminating  bool
	failedReason string
}

func newTransitionTracker(writer *RowWriter, metrics K8sWatchMetrics, scope string) *transitionTracker {
	if metrics == nil {
		metrics = noopK8sWatchMetrics{}
	}
	return &transitionTracker{
		writer:  writer,
		metrics: metrics,
		scope:   scope,
		last:    make(map[types.UID]podState),
	}
}

func (t *transitionTracker) handleUpsert(ctx context.Context, oldPod, newPod *corev1.Pod) {
	if !isManagedSessionPod(newPod) {
		return
	}
	owner := ownerEmail(newPod)
	sessionID := sessionID(newPod)
	if owner == "" || sessionID == "" {
		return
	}
	curr := derivePodState(newPod)
	t.mu.Lock()
	prev, hadPrev := t.last[newPod.UID]
	t.last[newPod.UID] = curr
	t.mu.Unlock()

	// First-sight (informer add or replica restart with existing pod):
	// emit a session.pod_scheduled row + whichever current condition row
	// reflects the live state. Idempotent via event_id.
	if !hadPrev {
		t.emit(ctx, scheduledEvent(t.scope, owner, sessionID, newPod))
		t.emitCurrentConditions(ctx, owner, sessionID, newPod, curr)
		return
	}

	// Transitions:
	if !prev.terminating && curr.terminating {
		t.emit(ctx, terminatingEvent(t.scope, owner, sessionID, newPod))
	}
	if prev.phase != corev1.PodFailed && prev.phase != corev1.PodSucceeded &&
		(curr.phase == corev1.PodFailed || curr.phase == corev1.PodSucceeded) {
		t.emit(ctx, failedEvent(t.scope, owner, sessionID, newPod, curr.failedReason))
		return
	}
	if prev.failedReason == "" && curr.failedReason != "" {
		t.emit(ctx, failedEvent(t.scope, owner, sessionID, newPod, curr.failedReason))
	}
	if prev.ready != curr.ready {
		if curr.ready {
			t.emit(ctx, readyEvent(t.scope, owner, sessionID, newPod))
		} else if curr.phase == corev1.PodRunning {
			t.emit(ctx, notReadyEvent(t.scope, owner, sessionID, newPod))
		}
	}
}

func (t *transitionTracker) emitCurrentConditions(ctx context.Context, owner, sessionID string, pod *corev1.Pod, curr podState) {
	if curr.terminating {
		t.emit(ctx, terminatingEvent(t.scope, owner, sessionID, pod))
		return
	}
	if curr.phase == corev1.PodFailed || curr.phase == corev1.PodSucceeded || curr.failedReason != "" {
		t.emit(ctx, failedEvent(t.scope, owner, sessionID, pod, curr.failedReason))
		return
	}
	if curr.ready {
		t.emit(ctx, readyEvent(t.scope, owner, sessionID, pod))
		return
	}
	if curr.phase == corev1.PodRunning {
		t.emit(ctx, notReadyEvent(t.scope, owner, sessionID, pod))
	}
}

// handleDelete clears the in-memory tracker entry when the informer
// reports a pod removed from etcd. The sessions row's deletion is
// owned by sessions.Manager.Delete via registry.MarkDeleted (visible
// flips false, row_version bumps, SPA drops the row). There is no
// K8s-watch row write on pod-fully-gone — by the time the informer
// fires, Manager has already published the deleted-row update, and
// any pod-terminating row update fired during graceful shutdown.
func (t *transitionTracker) handleDelete(_ context.Context, pod *corev1.Pod) {
	if !isManagedSessionPod(pod) {
		return
	}
	t.mu.Lock()
	delete(t.last, pod.UID)
	t.mu.Unlock()
}

func (t *transitionTracker) emit(ctx context.Context, event Event) {
	if event.Type == "" || event.SessionID == "" {
		return
	}
	outcome, err := t.writer.RecordTransition(ctx, event)
	if err != nil {
		slog.Warn("sessioncontroller k8s-watch: record transition failed",
			"session_id", event.SessionID,
			"type", event.Type,
			"error", err,
		)
		return
	}
	if outcome == TransitionNoOp {
		return
	}
	t.metrics.RecordTransition(event.Type)
	t.metricsForLag(event.OccurredAt)
}

// metricsForLag records the time between the producer-stamped
// occurred_at and now. Kept as its own method so the time.Parse-and-
// observe sequence has a single home.
func (t *transitionTracker) metricsForLag(occurredAt string) {
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return
	}
	lag := time.Since(parsed).Seconds()
	if lag < 0 {
		lag = 0
	}
	t.metrics.RecordLag(lag)
}

// --- pod state derivation -------------------------------------------------

func derivePodState(pod *corev1.Pod) podState {
	st := podState{
		phase: pod.Status.Phase,
	}
	if pod.DeletionTimestamp != nil {
		st.terminating = true
	}
	st.ready = isPodReady(pod)
	if reason := failureReason(pod); reason != "" {
		st.failedReason = reason
	}
	return st
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// failureReason returns the first "this pod is failed" reason it finds:
// pod.Status.Reason (set by eviction etc), CrashLoopBackOff on any
// container, or empty string when the pod is healthy.
func failureReason(pod *corev1.Pod) string {
	if pod.Status.Reason != "" && (pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded) {
		return pod.Status.Reason
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return "CrashLoopBackOff"
		}
	}
	return ""
}

// --- event builders -------------------------------------------------------

func scheduledEvent(scope, owner, sessionID string, pod *corev1.Pod) Event {
	occurredAt := pod.CreationTimestamp.UTC().Format(time.RFC3339Nano)
	return Event{
		Email:        owner,
		SessionScope: scope,
		SessionID:    sessionID,
		Type:         EventTypePodScheduled,
		OccurredAt:   occurredAt,
		Payload: map[string]any{
			"status":   "Pending",
			"pod_name": pod.Name,
			"pod_uid":  string(pod.UID),
		},
	}
}

func readyEvent(scope, owner, sessionID string, pod *corev1.Pod) Event {
	transitionAt := readyConditionTransitionTime(pod)
	readyAt := transitionAt.UTC().Format(time.RFC3339Nano)
	return Event{
		Email:        owner,
		SessionScope: scope,
		SessionID:    sessionID,
		Type:         EventTypePodReady,
		OccurredAt:   readyAt,
		Payload: map[string]any{
			"status":   "Active",
			"ready_at": readyAt,
			"pod_name": pod.Name,
			"pod_uid":  string(pod.UID),
		},
	}
}

func notReadyEvent(scope, owner, sessionID string, pod *corev1.Pod) Event {
	transitionAt := readyConditionTransitionTime(pod)
	return Event{
		Email:        owner,
		SessionScope: scope,
		SessionID:    sessionID,
		Type:         EventTypePodNotReady,
		OccurredAt:   transitionAt.UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{
			"status":   "Pending",
			"pod_name": pod.Name,
			"pod_uid":  string(pod.UID),
		},
	}
}

func failedEvent(scope, owner, sessionID string, pod *corev1.Pod, reason string) Event {
	exitCode, container, message := failureDetails(pod)
	if reason == "" {
		reason = "Failed"
	}
	payload := map[string]any{
		"status":   "Failed",
		"pod_name": pod.Name,
		"pod_uid":  string(pod.UID),
		"reason":   reason,
	}
	if container != "" {
		payload["container"] = container
	}
	if exitCode != 0 {
		payload["exit_code"] = exitCode
	}
	if message != "" {
		payload["message"] = message
	}
	occurredAt := pod.CreationTimestamp.UTC().Format(time.RFC3339Nano)
	if pod.Status.StartTime != nil {
		occurredAt = pod.Status.StartTime.UTC().Format(time.RFC3339Nano)
	}
	return Event{
		Email:        owner,
		SessionScope: scope,
		SessionID:    sessionID,
		Type:         EventTypePodFailed,
		OccurredAt:   occurredAt,
		Payload:      payload,
	}
}

func terminatingEvent(scope, owner, sessionID string, pod *corev1.Pod) Event {
	occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
	if pod.DeletionTimestamp != nil {
		occurredAt = pod.DeletionTimestamp.UTC().Format(time.RFC3339Nano)
	}
	return Event{
		Email:        owner,
		SessionScope: scope,
		SessionID:    sessionID,
		Type:         EventTypePodTerminating,
		OccurredAt:   occurredAt,
		Payload: map[string]any{
			"status":   "Failed",
			"pod_name": pod.Name,
			"pod_uid":  string(pod.UID),
		},
	}
}

// --- helpers --------------------------------------------------------------

func isManagedSessionPod(pod *corev1.Pod) bool {
	if pod == nil || pod.Labels == nil {
		return false
	}
	if pod.Labels["app.kubernetes.io/managed-by"] != "tank-operator" {
		return false
	}
	return pod.Labels["tank-operator/session-id"] != ""
}

func ownerEmail(pod *corev1.Pod) string {
	if pod == nil || pod.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(pod.Annotations["tank-operator/owner-email"])
}

func sessionID(pod *corev1.Pod) string {
	if pod == nil || pod.Labels == nil {
		return ""
	}
	return strings.TrimSpace(pod.Labels["tank-operator/session-id"])
}

func readyConditionTransitionTime(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.LastTransitionTime.Time
		}
	}
	// Fall back to CreationTimestamp — better than zero-time which would
	// produce a misleading order_key.
	return pod.CreationTimestamp.Time
}

func failureDetails(pod *corev1.Pod) (int32, string, string) {
	if pod == nil {
		return 0, "", ""
	}
	// Look for the highest-signal exit. We prefer terminated container
	// statuses over message-only failures.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return cs.State.Terminated.ExitCode, cs.Name, cs.State.Terminated.Message
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode != 0 {
			return cs.LastTerminationState.Terminated.ExitCode, cs.Name, cs.LastTerminationState.Terminated.Message
		}
	}
	if pod.Status.Message != "" {
		return 0, "", pod.Status.Message
	}
	return 0, "", ""
}

