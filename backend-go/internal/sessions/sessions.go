package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

const (
	defaultNamespace       = sessionmodel.SessionsNamespace
	nameAnnotation         = "tank-operator/display-name"
	testStateAnnotation    = "tank-operator/test-state"
	rolloutStateAnnotation = "tank-operator/rollout-state"
)

var (
	ErrNotFound = errors.New("session not found")
	ErrNotOwned = errors.New("session not owned")
)

type Info struct {
	ID           string                            `json:"id"`
	PodName      *string                           `json:"pod_name"`
	Owner        string                            `json:"owner"`
	Status       string                            `json:"status"`
	Mode         string                            `json:"mode"`
	RequestedAt  *string                           `json:"requested_at"`
	CreatedAt    *string                           `json:"created_at"`
	ReadyAt      *string                           `json:"ready_at"`
	Name         *string                           `json:"name"`
	TestState    map[string]any                    `json:"test_state"`
	RolloutState map[string]any                    `json:"rollout_state"`
	// Activity is the chat-derived sidebar indicator block. Populated by
	// the ListByOwner read path from the latest session.activity_changed
	// lifecycle event for this session; nil for sessions that haven't
	// produced any chat activity yet. Replaces the per-session response of
	// the deleted activity-polling endpoint.
	Activity *lifecycleevents.ActivitySummary `json:"activity,omitempty"`
}

// LifecycleStatusSource lets the Reader pull each session's durable
// pod-status snapshot from the lifecycle ledger so the `status` field on
// Info reflects the latest pod-state event instead of being recomputed
// from the live pod object on every List() call. Satisfied by
// lifecycleevents.Store.
type LifecycleStatusSource interface {
	LatestPodStatus(ctx context.Context, scope, sessionID string) (*lifecycleevents.PodStatusSummary, error)
	LatestActivity(ctx context.Context, scope, sessionID string) (*lifecycleevents.ActivitySummary, error)
}

type Reader struct {
	client    kubernetes.Interface
	namespace string
	registry  Registry
	lifecycle LifecycleStatusSource
	scope     string
	metrics   Metrics
}

// Registry is the read-only view of the session registry that Reader.List
// needs. The implementation must return BOTH visible and tombstoned
// (visible=false) records — Reader filters on Visible for the user-facing
// enumeration and uses the full set to distinguish a still-terminating
// pod owned by a known-deleted session (drop silently, the registry has
// already retired the row) from an orphan pod that the registry never
// knew about (drop, but increment the orphan counter — a steady-state
// signal that a session pod escaped its registry row, which means the
// pod-deletion / registry-deletion paths have diverged).
type Registry interface {
	List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error)
}

// Metrics is the optional observability hook the Reader uses to surface
// list-side anomalies. RecordOrphanPod fires once per pod observed by the
// Kubernetes API that has no matching registry row (visible or
// tombstoned). Steady-state expectation is zero. Non-zero means either
// (a) a session pod was created outside Manager.Create (real
// architectural bug), or (b) the registry entry was wiped while the pod
// kept running (the reaper should be the only thing in that path; if
// this counter ticks, the reaper grew a leak). Either way the dashboard
// alert is "investigate", not "auto-recover" — the Reader intentionally
// no longer surfaces orphan pods to the user-facing list.
type Metrics interface {
	RecordOrphanPod()
}

type noopMetrics struct{}

func (noopMetrics) RecordOrphanPod() {}

// NewReader builds a Reader that supports only Get-side lookups. List
// will fail with ErrRegistryRequired because, post-#83, the registry is
// the durable enumeration source. Tests that exercise Get-only paths can
// keep using this constructor; production callers wire NewReaderFull.
func NewReader(client kubernetes.Interface, namespace string) *Reader {
	return NewReaderWithRegistry(client, namespace, nil)
}

// NewReaderWithRegistry builds a Reader with a registry but without the
// lifecycle ledger. Same use case as NewReader — Get-side lookups in
// tests. List still requires a registry to enumerate from but does not
// require the lifecycle ledger (Status falls back to the
// infoFromRecord-derived value).
func NewReaderWithRegistry(client kubernetes.Interface, namespace string, registry Registry) *Reader {
	return NewReaderFull(client, namespace, registry, nil, "")
}

// NewReaderFull is the production constructor. The registry is the
// authoritative session enumeration; the lifecycle ledger hydrates each
// row's Status and Activity. Wire metrics via WithMetrics to surface the
// orphan-pod counter.
func NewReaderFull(client kubernetes.Interface, namespace string, registry Registry, lifecycle LifecycleStatusSource, scope string) *Reader {
	if namespace == "" {
		namespace = defaultNamespace
	}
	if scope == "" {
		scope = "default"
	}
	return &Reader{
		client:    client,
		namespace: namespace,
		registry:  registry,
		lifecycle: lifecycle,
		scope:     scope,
		metrics:   noopMetrics{},
	}
}

// WithMetrics wires an observability adapter. Returns the receiver so
// callers can chain off the constructor.
func (r *Reader) WithMetrics(metrics Metrics) *Reader {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	r.metrics = metrics
	return r
}

// ErrRegistryRequired is returned by List when the Reader has no
// registry wired. Post-#83 the registry is the durable session
// enumeration; there is no fall-through to pod-listing as the
// enumeration source. Get-only Readers (legacy NewReader without a
// registry) intentionally fail loud here rather than silently degrading
// to the retired pod-only path.
var ErrRegistryRequired = errors.New("sessions: registry is required for List")

// List returns the per-owner session enumeration from the durable
// registry, hydrated with each session's latest Status/Activity from the
// lifecycle ledger. Pods are looked up by session_id for additional
// hydration (annotations, ready_at) of registry rows that have a live
// pod, but the pod listing is never the source of enumeration — a pod
// the registry doesn't know about is an orphan, counted but not
// surfaced. A pod owned by a tombstoned registry row (visible=false) is
// dropped silently; the registry already retired the row.
//
// This shape replaces the pre-fix pod-loop that re-added still-
// terminating pods after Manager.Delete had already MarkDeleted them, so
// the SPA snapshot lied about the deletion and the sidebar got "stuck
// deleting" rows once the SSE session.deleted had cleared the entry. Per
// docs/product-inspirations.md: user-visible state comes from durable
// events, not from a runtime read whose purpose is to keep an old code
// path alive.
func (r *Reader) List(ctx context.Context, owner string) ([]Info, error) {
	if r.registry == nil {
		return nil, ErrRegistryRequired
	}

	records, err := r.registry.List(ctx, owner)
	if err != nil {
		return nil, err
	}

	ownerLabel := sessionmodel.OwnerLabel(owner)
	pods, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "tank-operator/owner=" + ownerLabel,
	})
	if err != nil {
		return nil, err
	}
	podsByID := make(map[string]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		podsByID[sessionIDFromPod(pod)] = pod
	}

	// known is the full set of session_ids the registry has seen for this
	// owner (visible OR tombstoned). Used below to classify each pod:
	// known-and-visible → already enumerated from the registry loop;
	// known-and-tombstoned → drop silently (registry retired the row);
	// unknown → orphan, count and drop.
	known := make(map[string]struct{}, len(records))
	out := make([]Info, 0, len(records))
	for _, record := range records {
		known[record.ID] = struct{}{}
		if !record.Visible {
			continue
		}
		info := infoFromRecord(owner, record, podsByID[record.ID])
		r.hydrateLifecycle(ctx, &info)
		out = append(out, info)
	}

	for _, pod := range pods.Items {
		id := sessionIDFromPod(&pod)
		if _, ok := known[id]; ok {
			continue
		}
		if !podHasSandboxAgent(&pod) {
			continue
		}
		r.metrics.RecordOrphanPod()
	}

	return out, nil
}

// hydrateLifecycle replaces the live-pod status computation with the
// durable equivalent: the latest session.pod_* event drives Status (and
// ReadyAt where applicable), and the latest session.activity_changed
// fills the Activity block. Falls back to whatever Status the
// infoFromRecord/infoFromPod path already set if the lifecycle store is
// unwired (local dev with stub store) or hasn't seen the session yet.
func (r *Reader) hydrateLifecycle(ctx context.Context, info *Info) {
	if r.lifecycle == nil || info == nil || info.ID == "" {
		return
	}
	if status, err := r.lifecycle.LatestPodStatus(ctx, r.scope, info.ID); err == nil && status != nil {
		if status.Status != "" {
			info.Status = status.Status
		}
		if status.ReadyAt != nil {
			info.ReadyAt = status.ReadyAt
		}
	}
	if activity, err := r.lifecycle.LatestActivity(ctx, r.scope, info.ID); err == nil && activity != nil {
		copy := *activity
		info.Activity = &copy
	}
}

func (r *Reader) Get(ctx context.Context, owner, sessionID string) (Info, error) {
	pod, err := r.client.CoreV1().Pods(r.namespace).Get(ctx, "session-"+sessionID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		pods, listErr := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "tank-operator/session-id=" + sessionID,
		})
		if listErr != nil {
			return Info{}, listErr
		}
		if len(pods.Items) == 0 {
			return Info{}, ErrNotFound
		}
		pod = &pods.Items[0]
		err = nil
	}
	if err != nil {
		return Info{}, err
	}
	if pod.Labels["tank-operator/owner"] != sessionmodel.OwnerLabel(owner) {
		return Info{}, ErrNotOwned
	}
	return infoFromPod(owner, pod), nil
}

// GetByID resolves a session by ID without verifying ownership. The
// returned Info carries the resolved owner email so the HTTP layer can
// authorize the caller against it (admin → allowed for any owner;
// non-admin → must match their own email). Only the read-side handlers
// reach for this — write paths continue to call Get(owner, sessionID)
// so an admin token can't submit turns / write files / attach
// terminals into someone else's session.
//
// Returns ErrNotFound if no pod for sessionID exists. The
// `tank-operator/owner-email` annotation is the source of truth for
// the email (the `tank-operator/owner` label is a one-way hash sized
// for k8s label constraints and can't be reversed). Pods missing the
// annotation are treated as not-found rather than returning an empty
// owner — a half-tagged pod can't be authorized either way and would
// otherwise silently grant access to any caller whose email is also
// empty.
func (r *Reader) GetByID(ctx context.Context, sessionID string) (Info, error) {
	pod, err := r.client.CoreV1().Pods(r.namespace).Get(ctx, "session-"+sessionID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		pods, listErr := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "tank-operator/session-id=" + sessionID,
		})
		if listErr != nil {
			return Info{}, listErr
		}
		if len(pods.Items) == 0 {
			return Info{}, ErrNotFound
		}
		pod = &pods.Items[0]
		err = nil
	}
	if err != nil {
		return Info{}, err
	}
	owner := strings.TrimSpace(pod.Annotations["tank-operator/owner-email"])
	if owner == "" {
		return Info{}, ErrNotFound
	}
	return infoFromPod(owner, pod), nil
}

func infoFromRecord(owner string, record sessionmodel.SessionRecord, pod *corev1.Pod) Info {
	if pod != nil {
		info := infoFromPod(owner, pod)
		info.ID = record.ID
		info.Mode = sessionmodel.NormalizeSessionMode(record.Mode)
		info.RequestedAt = firstString(record.RequestedAt, record.CreatedAt, valueString(info.RequestedAt))
		info.CreatedAt = firstString(record.CreatedAt, valueString(info.CreatedAt))
		info.Name = record.Name
		return info
	}
	return Info{
		ID:          record.ID,
		PodName:     optionalString(record.PodName),
		Owner:       owner,
		Status:      "Failed",
		Mode:        sessionmodel.NormalizeSessionMode(record.Mode),
		RequestedAt: firstString(record.RequestedAt, record.CreatedAt),
		CreatedAt:   optionalString(record.CreatedAt),
		ReadyAt:     nil,
		Name:        record.Name,
	}
}

// infoFromPod builds an Info from a live pod. Used by Reader.Get for
// per-session lookups (pod is the cheapest source of pod-derived
// annotations); Reader.List no longer flows here as the enumeration
// source — the registry is, per the comment on Reader.List. The Status
// field defaults to "Pending" — the real Status comes from
// hydrateLifecycle's pull against the latest session.pod_* lifecycle
// event. Live pod-state computation is intentionally NOT done here per
// tank-operator#83: status is derived from the durable ledger, not the
// pod object.
func infoFromPod(owner string, pod *corev1.Pod) Info {
	podName := pod.Name
	createdAt := timeString(pod.CreationTimestamp.Time)
	readyAt := readyAt(pod)
	name := annotationString(pod.Annotations, nameAnnotation)
	return Info{
		ID:           sessionIDFromPod(pod),
		PodName:      &podName,
		Owner:        owner,
		Status:       "Pending",
		Mode:         sessionmodel.NormalizeSessionMode(pod.Labels["tank-operator/mode"]),
		RequestedAt:  createdAt,
		CreatedAt:    createdAt,
		ReadyAt:      readyAt,
		Name:         name,
		TestState:    annotationObject(pod.Annotations, testStateAnnotation),
		RolloutState: annotationObject(pod.Annotations, rolloutStateAnnotation),
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func firstString(values ...string) *string {
	for _, value := range values {
		if value != "" {
			return &value
		}
	}
	return nil
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sessionIDFromPod(pod *corev1.Pod) string {
	if pod.Labels != nil && pod.Labels["tank-operator/session-id"] != "" {
		return pod.Labels["tank-operator/session-id"]
	}
	return strings.TrimPrefix(pod.Name, "session-")
}

func podHasSandboxAgent(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name != "claude" {
			continue
		}
		for _, port := range container.Ports {
			if port.Name == "sandbox-agent" {
				return true
			}
		}
		return false
	}
	return false
}

// podStatus was deleted in tank-operator#83. Status is derived from the
// session_lifecycle_events ledger via LatestPodStatus, not computed live
// from the pod object. See Reader.hydrateLifecycle and the
// scripts/check-removed-chat-runtime.mjs guard.

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning || len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready {
			return false
		}
	}
	return true
}

func readyAt(pod *corev1.Pod) *string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return timeString(condition.LastTransitionTime.Time)
		}
	}
	return nil
}

func timeString(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format("2006-01-02T15:04:05+00:00")
	return &value
}

func annotationString(annotations map[string]string, key string) *string {
	if annotations == nil || annotations[key] == "" {
		return nil
	}
	value := annotations[key]
	return &value
}

func annotationObject(annotations map[string]string, key string) map[string]any {
	if annotations == nil || annotations[key] == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(annotations[key]), &out); err != nil {
		return nil
	}
	return out
}
