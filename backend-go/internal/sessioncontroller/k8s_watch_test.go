package sessioncontroller

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// fakeEmitter is the RowEmitter test double — shared with
// writer_test.go in the same package. Records each PublishCurrentRow
// call so tests can assert on (owner, sessionID) pairs.
type fakeEmitter struct {
	mu    sync.Mutex
	calls []emittedRow
}

type emittedRow struct {
	owner     string
	sessionID string
}

func (e *fakeEmitter) PublishCurrentRow(_ context.Context, owner, sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, emittedRow{owner: owner, sessionID: sessionID})
}

// newTestTracker returns a transitionTracker whose writer records
// each publish into the returned eventRecorder. No Postgres pool is
// wired; per-event-type column-update behavior is tested in
// writer_test.go, while these tests pin the K8s watch's transition-
// detection logic (which event-builders fire on which pod-state
// changes).
func newTestTracker() (*transitionTracker, *eventRecorder) {
	rec := &eventRecorder{}
	writer := &RowWriter{
		Emitter: &recordingEmitter{rec: rec},
		Pool:    nil,
		Metrics: noopRowWriterMetrics{},
	}
	tracker := &transitionTracker{
		metrics: noopK8sWatchMetrics{},
		scope:   "default",
		last:    make(map[types.UID]podState),
		writer:  writer,
	}
	return tracker, rec
}

type terminationMetricRecorder struct {
	noopK8sWatchMetrics
	mu    sync.Mutex
	calls []containerTermination
}

func (r *terminationMetricRecorder) RecordContainerTermination(container, reason string, exitCode int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, containerTermination{
		container: container,
		reason:    reason,
		exitCode:  exitCode,
	})
}

func (r *terminationMetricRecorder) all() []containerTermination {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]containerTermination, len(r.calls))
	copy(out, r.calls)
	return out
}

type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) record(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// recordingEmitter captures each publish target. The K8s watch tests
// don't need to see the upstream Event type — the event-builder
// functions (scheduledEvent / readyEvent / failedEvent) are pure and
// can be exercised inline in the test body for type assertions.
type recordingEmitter struct {
	rec *eventRecorder
}

func (e *recordingEmitter) PublishCurrentRow(_ context.Context, owner, sessionID string) {
	e.rec.record(Event{Email: owner, SessionID: sessionID})
}

func TestHandleUpsertEmitsScheduledOnFirstSight(t *testing.T) {
	tracker, rec := newTestTracker()

	pod := newSessionPod("21", "u@example.com", corev1.PodPending, false)
	tracker.handleUpsert(context.Background(), nil, pod)

	calls := rec.all()
	if len(calls) == 0 {
		t.Fatalf("expected publish calls, got %d", len(calls))
	}
	if got := calls[0].SessionID; got != "21" {
		t.Fatalf("first publish session = %q, want 21", got)
	}

	wantScheduled := scheduledEvent("default", "u@example.com", "21", pod)
	if wantScheduled.Type != EventTypePodScheduled {
		t.Fatalf("scheduledEvent type = %q, want %q", wantScheduled.Type, EventTypePodScheduled)
	}
}

func TestHandleUpsertEmitsReadyOnTransition(t *testing.T) {
	tracker, rec := newTestTracker()

	pending := newSessionPod("21", "u@example.com", corev1.PodPending, false)
	tracker.handleUpsert(context.Background(), nil, pending)
	priorCount := len(rec.all())

	ready := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	ready.UID = pending.UID
	tracker.handleUpsert(context.Background(), pending, ready)

	if got := len(rec.all()); got <= priorCount {
		t.Fatalf("transition publish count = %d, want > %d (ready transition must publish)", got, priorCount)
	}

	want := readyEvent("default", "u@example.com", "21", ready)
	if want.Type != EventTypePodReady {
		t.Fatalf("readyEvent type = %q, want %q", want.Type, EventTypePodReady)
	}
	if want.Payload["status"] != "Active" {
		t.Fatalf("readyEvent payload.status = %v, want Active", want.Payload["status"])
	}
}

func TestHandleUpsertEmitsFailedOnEviction(t *testing.T) {
	tracker, rec := newTestTracker()

	running := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	tracker.handleUpsert(context.Background(), nil, running)
	priorCount := len(rec.all())

	evicted := running.DeepCopy()
	evicted.Status.Phase = corev1.PodFailed
	evicted.Status.Reason = "Evicted"
	evicted.Status.Message = "The node was low on resource: memory."
	evicted.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "claude-runner",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Message: "OOMKilled"},
		},
	}}
	tracker.handleUpsert(context.Background(), running, evicted)

	if got := len(rec.all()); got <= priorCount {
		t.Fatalf("eviction publish count = %d, want > %d", got, priorCount)
	}

	want := failedEvent("default", "u@example.com", "21", evicted, failureReason(evicted))
	if want.Type != EventTypePodFailed {
		t.Fatalf("failedEvent type = %q, want %q", want.Type, EventTypePodFailed)
	}
	if want.Payload["status"] != "Failed" {
		t.Fatalf("failedEvent payload.status = %v, want Failed", want.Payload["status"])
	}
	if want.Payload["reason"] != "Evicted" {
		t.Fatalf("failedEvent payload.reason = %v, want Evicted", want.Payload["reason"])
	}
	if want.Payload["exit_code"] != int32(137) {
		t.Fatalf("failedEvent payload.exit_code = %v, want 137", want.Payload["exit_code"])
	}
	if want.Payload["container"] != "claude-runner" {
		t.Fatalf("failedEvent payload.container = %v, want claude-runner", want.Payload["container"])
	}
}

func TestHandleUpsertRecordsContainerOOMTermination(t *testing.T) {
	recorder := &terminationMetricRecorder{}
	tracker, _ := newTestTracker()
	tracker.metrics = recorder

	running := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	tracker.handleUpsert(context.Background(), nil, running)

	restarted := running.DeepCopy()
	restarted.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "claude", Ready: true},
		{
			Name:         "codex-runner",
			Ready:        true,
			RestartCount: 1,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:     "OOMKilled",
					ExitCode:   137,
					FinishedAt: metav1.NewTime(time.Date(2026, 6, 7, 18, 59, 28, 0, time.UTC)),
				},
			},
		},
		{Name: "mcp-auth-proxy", Ready: true},
	}
	tracker.handleUpsert(context.Background(), running, restarted)
	tracker.handleUpsert(context.Background(), restarted, restarted)

	calls := recorder.all()
	if len(calls) != 1 {
		t.Fatalf("termination metric calls = %d, want 1", len(calls))
	}
	if calls[0].container != "codex-runner" {
		t.Fatalf("container = %q, want codex-runner", calls[0].container)
	}
	if calls[0].reason != "oom_killed" {
		t.Fatalf("reason = %q, want oom_killed", calls[0].reason)
	}
	if calls[0].exitCode != 137 {
		t.Fatalf("exitCode = %d, want 137", calls[0].exitCode)
	}
}

// TestHandleDeleteClearsTrackerState pins the new K8s watch
// contract: pod-fully-gone is a no-op on the row-write path. Manager
// .Delete owns deletion (visible=false + row_version bump via
// registry.MarkDeleted, fans through RowPublisher); the watch's only
// responsibility is clearing the in-memory last-state map so a future
// pod with the same UID re-fires scheduledEvent rather than treating
// it as a continuation.
func TestHandleDeleteClearsTrackerState(t *testing.T) {
	tracker, rec := newTestTracker()

	pod := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	tracker.handleUpsert(context.Background(), nil, pod)
	publishesAfterUpsert := len(rec.all())

	tracker.handleDelete(context.Background(), pod)

	if got := len(rec.all()); got != publishesAfterUpsert {
		t.Fatalf("handleDelete publish count = %d, want %d (handleDelete must not publish)", got, publishesAfterUpsert)
	}
	tracker.mu.Lock()
	_, present := tracker.last[pod.UID]
	tracker.mu.Unlock()
	if present {
		t.Fatalf("handleDelete left tracker.last entry behind, want cleared")
	}
}

func TestIgnoresUnrelatedPods(t *testing.T) {
	tracker, rec := newTestTracker()

	unrelated := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	unrelated.Labels = map[string]string{} // strip session/managed labels
	tracker.handleUpsert(context.Background(), nil, unrelated)

	if got := len(rec.all()); got != 0 {
		t.Fatalf("unmanaged pod must not produce row publishes, got %d", got)
	}
}

// --- helpers --------------------------------------------------------------

func newSessionPod(id, owner string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	created := metav1.NewTime(time.Date(2026, 5, 16, 0, 0, 1, 0, time.UTC))
	readyTime := metav1.NewTime(time.Date(2026, 5, 16, 0, 0, 3, 0, time.UTC))
	statuses := []corev1.ContainerStatus{
		{Name: "claude", Ready: ready},
		{Name: "claude-runner", Ready: ready},
		{Name: "mcp-auth-proxy", Ready: ready},
	}
	conditions := []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             condStatus(ready),
		LastTransitionTime: readyTime,
	}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "session-" + id,
			Namespace:         sessionmodel.SessionsNamespace,
			UID:               types.UID("uid-" + id),
			CreationTimestamp: created,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tank-operator",
				"tank-operator/owner":          sessionmodel.OwnerLabel(owner),
				"tank-operator/session-id":     id,
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": owner,
			},
		},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
			Conditions:        conditions,
		},
	}
}

func condStatus(ready bool) corev1.ConditionStatus {
	if ready {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

// --- crash-loop backstop --------------------------------------------------
//
// The restart-budget backstop reaps a session pod whose agent-runner container
// is stuck in CrashLoopBackOff past the budget, marking the session
// provider_fatal{reason:"runner_crashloop"}. It is the defense-in-depth that
// guarantees no session-pod container crash-loops unbounded (session 979 looped
// until it would have hit the ~24h idle reaper). Reaping the pod is what
// actually stops the kubelet restartPolicy=Always loop.

type reapRecorder struct {
	mu   sync.Mutex
	pods []string
}

func (r *reapRecorder) reap(_ context.Context, pod *corev1.Pod) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pods = append(r.pods, pod.Name)
	return nil
}

func (r *reapRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pods)
}

type reapMetricRecorder struct {
	noopK8sWatchMetrics
	mu      sync.Mutex
	reasons []string
}

func (r *reapMetricRecorder) RecordPodReaped(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
}

func (r *reapMetricRecorder) reapedReasons() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.reasons))
	copy(out, r.reasons)
	return out
}

func newBackstopTracker(budget int32) (*transitionTracker, *reapRecorder, *reapMetricRecorder) {
	rec := &eventRecorder{}
	writer := &RowWriter{
		Emitter: &recordingEmitter{rec: rec},
		Pool:    nil,
		Metrics: noopRowWriterMetrics{},
	}
	reaper := &reapRecorder{}
	metrics := &reapMetricRecorder{}
	tracker := &transitionTracker{
		metrics:         metrics,
		scope:           "default",
		last:            make(map[types.UID]podState),
		crashloopReaped: make(map[types.UID]struct{}),
		crashloopBudget: budget,
		writer:          writer,
		reap:            reaper.reap,
	}
	return tracker, reaper, metrics
}

// newCrashloopPod returns a Running pod whose claude-runner container is in
// CrashLoopBackOff with the given restart count.
func newCrashloopPod(id, owner string, restartCount int32) *corev1.Pod {
	pod := newSessionPod(id, owner, corev1.PodRunning, false)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "claude", Ready: true},
		{
			Name:         "claude-runner",
			Ready:        false,
			RestartCount: restartCount,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
			},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
			},
		},
		{Name: "mcp-auth-proxy", Ready: true},
	}
	return pod
}

func TestBackstopReapsCrashloopingRunnerPastBudget(t *testing.T) {
	tracker, reaper, metrics := newBackstopTracker(5)
	pod := newCrashloopPod("979", "nelson@romaine.life", 5)

	tracker.handleUpsert(context.Background(), nil, pod)

	if reaper.count() != 1 {
		t.Fatalf("reap calls = %d, want 1", reaper.count())
	}
	if reasons := metrics.reapedReasons(); len(reasons) != 1 || reasons[0] != "runner_crashloop" {
		t.Fatalf("reaped reasons = %v, want [runner_crashloop]", reasons)
	}

	// Fires at most once per pod: a second observation of the same crash-looping
	// pod must not reap again (the row is already terminally Failed).
	tracker.handleUpsert(context.Background(), pod, pod)
	if reaper.count() != 1 {
		t.Fatalf("reap calls after second observation = %d, want 1 (once per pod)", reaper.count())
	}
}

func TestBackstopDoesNotReapUnderBudget(t *testing.T) {
	tracker, reaper, _ := newBackstopTracker(5)
	pod := newCrashloopPod("44", "u@example.com", 4)

	tracker.handleUpsert(context.Background(), nil, pod)

	if reaper.count() != 0 {
		t.Fatalf("reap calls = %d, want 0 (restart count under budget)", reaper.count())
	}
}

func TestBackstopDoesNotReapRecoveredRunner(t *testing.T) {
	tracker, reaper, _ := newBackstopTracker(5)
	// 12 lifetime restarts but the runner is currently Running and Ready — a
	// session that flapped early then stabilized must NOT be reaped.
	pod := newSessionPod("45", "u@example.com", corev1.PodRunning, true)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "claude", Ready: true},
		{
			Name:         "claude-runner",
			Ready:        true,
			RestartCount: 12,
			State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		},
		{Name: "mcp-auth-proxy", Ready: true},
	}

	tracker.handleUpsert(context.Background(), nil, pod)

	if reaper.count() != 0 {
		t.Fatalf("reap calls = %d, want 0 (recovered runner, not currently looping)", reaper.count())
	}
}

func TestBackstopDisabledWithoutReaper(t *testing.T) {
	tracker, _ := newTestTracker() // reap == nil
	pod := newCrashloopPod("46", "u@example.com", 99)
	// Must not panic and must fall through to the normal transition path.
	tracker.handleUpsert(context.Background(), nil, pod)
}

// TestCrashloopFatalEventIsStickyTerminal pins that the backstop's event reuses
// the provider-fatal transition: a Failed row that is sticky (terminating_at
// set) so a doomed-but-briefly-ready container can't flip it back to Active.
func TestCrashloopFatalEventIsStickyTerminal(t *testing.T) {
	pod := newCrashloopPod("979", "nelson@romaine.life", 7)
	ev := crashloopFatalEvent("default", "nelson@romaine.life", "979", pod, 7)

	if ev.Type != EventTypeProviderFatal {
		t.Fatalf("type = %q, want %q", ev.Type, EventTypeProviderFatal)
	}
	if ev.Payload["reason"] != "runner_crashloop" {
		t.Fatalf("reason = %v, want runner_crashloop", ev.Payload["reason"])
	}
	if ev.Payload["restart_count"] != int32(7) {
		t.Fatalf("restart_count = %v, want int32(7)", ev.Payload["restart_count"])
	}

	changes, ok := deriveRowColumnChanges(ev)
	if !ok || changes.status != "Failed" {
		t.Fatalf("provider_fatal row = %+v ok=%v, want status Failed", changes, ok)
	}
	if changes.terminatingAt == nil {
		t.Fatalf("provider_fatal must set terminating_at (sticky terminal)")
	}
}
