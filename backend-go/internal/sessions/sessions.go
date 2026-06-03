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

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	defaultNamespace       = sessionmodel.SessionsNamespace
	nameAnnotation         = "tank-operator/display-name"
	capabilitiesAnnotation = "tank-operator/capabilities"
	testStateAnnotation    = "tank-operator/test-state"
	rolloutStateAnnotation = "tank-operator/rollout-state"
)

var (
	ErrNotFound = errors.New("session not found")
	ErrNotOwned = errors.New("session not owned")
)

type Info struct {
	ID           string         `json:"id"`
	SessionScope string         `json:"session_scope,omitempty"`
	PodName      *string        `json:"pod_name"`
	Owner        string         `json:"owner"`
	Status       string         `json:"status"`
	Mode         string         `json:"mode"`
	RequestedAt  *string        `json:"requested_at"`
	CreatedAt    *string        `json:"created_at"`
	ReadyAt      *string        `json:"ready_at"`
	Name         *string        `json:"name"`
	TestState    map[string]any `json:"test_state"`
	RolloutState map[string]any `json:"rollout_state"`
	// Repos is the "owner/name" slug list the user picked at
	// session creation; always present on the wire (empty array
	// when none were selected). Driven by the durable
	// sessions.repos column, not local SPA state, so the splash
	// chips and the per-session detail view both read through to
	// the same source. The repo-cloner init container reads this
	// same list from the pod side.
	Repos []string `json:"repos"`
	// CloneState carries the per-repo init-container outcome the
	// repo-cloner writes back to sessions.clone_state. nil until
	// the cloner publishes its first state. Omitted from the wire when
	// nil to keep the snapshot lean for the every-row today shape.
	CloneState map[string]any `json:"clone_state,omitempty"`
	// Capabilities is the durable per-session capability list selected at
	// create time. Empty means the default pod surface.
	Capabilities []string `json:"capabilities"`
	// RowVersion is the per-(owner, scope) monotonic cursor each
	// sessions row carries (docs/session-list-redesign.md Phase 1).
	// The SPA's SessionStore reads this to seed its EventSource
	// cursor on snapshot bootstrap so the row-update catch-up only
	// emits changes that landed AFTER the snapshot.
	RowVersion int64 `json:"row_version"`
	// SidebarPosition is the durable sort key for the sidebar. Larger
	// values render earlier; row_version updates must not affect it.
	SidebarPosition int64 `json:"sidebar_position"`
	// Activity is the chat-derived sidebar indicator block. Sourced
	// from the sessions.activity_summary column (Phase 2);
	// nil for sessions that haven't produced any chat activity yet.
	Activity *sessionactivity.ActivitySummary `json:"activity,omitempty"`
	// Model/Effort are the durable session-owned run options. Runtime*
	// fields are written back by the runner after it applies the config
	// to the provider executable/SDK.
	Model                          string  `json:"model,omitempty"`
	Effort                         string  `json:"effort,omitempty"`
	RuntimeModel                   string  `json:"runtime_model,omitempty"`
	RuntimeEffort                  string  `json:"runtime_effort,omitempty"`
	RuntimeConfiguredAt            *string `json:"runtime_configured_at,omitempty"`
	RuntimeContextWindowTokens     int64   `json:"runtime_context_window_tokens,omitempty"`
	RuntimeContextWindowSource     string  `json:"runtime_context_window_source,omitempty"`
	RuntimeContextWindowObservedAt *string `json:"runtime_context_window_observed_at,omitempty"`
	AgentAvatarID                  string  `json:"agent_avatar_id,omitempty"`
	SystemAvatarID                 string  `json:"system_avatar_id,omitempty"`
}

type Reader struct {
	client    kubernetes.Interface
	namespace string
	registry  Registry
	scope     string
}

type Registry interface {
	List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error)
}

func NewReader(client kubernetes.Interface, namespace string) *Reader {
	return NewReaderWithRegistry(client, namespace, nil)
}

func NewReaderWithRegistry(client kubernetes.Interface, namespace string, registry Registry) *Reader {
	return NewReaderFull(client, namespace, registry, "")
}

// NewReaderFull is the full-fledged constructor. The pre-Phase-2
// lifecycle hydration parameter is gone — the snapshot reads Status
// / ReadyAt / Activity directly from the sessions row columns now,
// no ledger pull on the read path.
func NewReaderFull(client kubernetes.Interface, namespace string, registry Registry, scope string) *Reader {
	if namespace == "" {
		namespace = defaultNamespace
	}
	if scope == "" {
		scope = "default"
	}
	return &Reader{client: client, namespace: namespace, registry: registry, scope: scope}
}

// List returns the sidebar snapshot for one owner. Phase 2 of
// docs/session-list-redesign.md cut this from a three-source merge
// (K8s pods + registry + lifecycle store hydration) down to a single
// registry read — the row carries every column the SPA renders.
//
// The K8s pod list is no longer read here: every field the snapshot
// needs (status, ready_at, terminating_at, activity_summary,
// test_state, rollout_state) lives on the sessions row, populated by
// sessioncontroller.RowWriter and the registry write methods.
// Dropping the pod read also eliminates the pod-fallback loop, which
// was the bug that let Terminating pods for just-deleted sessions
// reappear in the sidebar for the full ~75s graceful-shutdown window
// (tank-operator#525 Bug A — the surface symptom that drove the
// redesign).
//
// Registry-only mode (no registry wired): returns empty. Local-dev
// shape; production always has a registry.
func (r *Reader) List(ctx context.Context, owner string) ([]Info, error) {
	if r.registry == nil {
		return nil, nil
	}
	records, err := r.registry.List(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(records))
	for _, record := range records {
		if !record.Visible {
			continue
		}
		out = append(out, infoFromRecord(owner, record))
	}
	return out, nil
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

// infoFromRecord builds the snapshot Info entirely from a sessions
// row. All sidebar-visible fields live on the row as of Phase 2; the
// K8s pod is no longer consulted on the snapshot path. Activity is
// parsed from the activity_summary jsonb column written by the
// chat-activity emitter on each indicator-affecting chat event.
func infoFromRecord(owner string, record sessionmodel.SessionRecord) Info {
	status := record.Status
	if status == "" {
		// Brand-new rows whose status hasn't been written yet: render
		// as Pending. The Phase 1 schema default also stamps 'Pending'
		// at INSERT time, so this branch only fires for synthetic
		// records (tests with empty fixtures).
		status = "Pending"
	}
	// Repos defaults to an empty slice on the wire so the SPA never
	// has to distinguish "field absent" from "no repos picked" —
	// they're the same product state.
	repos := record.Repos
	if repos == nil {
		repos = []string{}
	}
	capabilities := record.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	scope := strings.TrimSpace(record.Scope)
	if scope == "" {
		scope = "default"
	}
	info := Info{
		ID:                             record.ID,
		SessionScope:                   scope,
		PodName:                        optionalString(record.PodName),
		Owner:                          owner,
		Status:                         status,
		Mode:                           sessionmodel.NormalizeSessionMode(record.Mode),
		RequestedAt:                    firstString(record.RequestedAt, record.CreatedAt),
		CreatedAt:                      optionalString(record.CreatedAt),
		ReadyAt:                        optionalString(record.ReadyAt),
		Name:                           record.Name,
		TestState:                      record.TestState,
		RolloutState:                   record.RolloutState,
		Repos:                          repos,
		CloneState:                     record.CloneState,
		Capabilities:                   capabilities,
		RowVersion:                     record.RowVersion,
		SidebarPosition:                record.SidebarPosition,
		Model:                          record.Model,
		Effort:                         record.Effort,
		RuntimeModel:                   record.RuntimeModel,
		RuntimeEffort:                  record.RuntimeEffort,
		RuntimeConfiguredAt:            optionalString(record.RuntimeConfiguredAt),
		RuntimeContextWindowTokens:     record.RuntimeContextWindowTokens,
		RuntimeContextWindowSource:     record.RuntimeContextWindowSource,
		RuntimeContextWindowObservedAt: optionalString(record.RuntimeContextWindowObservedAt),
		AgentAvatarID:                  record.AgentAvatarID,
		SystemAvatarID:                 record.SystemAvatarID,
	}
	if activity := parseActivitySummary(record.ActivitySummary); activity != nil {
		info.Activity = activity
	}
	return info
}

// InfoFromRecord exposes the registry-row projection for read-only handlers
// that need to inspect a scope different from the manager's write scope.
func InfoFromRecord(owner string, record sessionmodel.SessionRecord) Info {
	return infoFromRecord(owner, record)
}

// parseActivitySummary decodes the row's activity_summary jsonb into
// the sessionactivity.ActivitySummary the Info field expects. Empty
// columns return nil so the sidebar renders "no activity yet" for
// fresh sessions.
func parseActivitySummary(raw []byte) *sessionactivity.ActivitySummary {
	if len(raw) == 0 {
		return nil
	}
	var out sessionactivity.ActivitySummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}

// infoFromPod builds an Info from a live pod. After Phase 2 of
// docs/session-list-redesign.md this is only called by Reader.Get
// (per-session detail page); Reader.List goes straight to the row.
// Status defaults to "Pending" here because the row's value already
// reflects the latest lifecycle transition by the time the per-
// session GET handler runs and the SPA snapshot view always wins on
// the sidebar.
func infoFromPod(owner string, pod *corev1.Pod) Info {
	podName := pod.Name
	createdAt := timeString(pod.CreationTimestamp.Time)
	readyAt := readyAt(pod)
	name := annotationString(pod.Annotations, nameAnnotation)
	scope := strings.TrimSpace(pod.Labels["tank-operator/session-scope"])
	if scope == "" {
		scope = "default"
	}
	return Info{
		ID:           sessionIDFromPod(pod),
		SessionScope: scope,
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
		// Pod-only Info (per-session GET fallback when the registry
		// row isn't reachable) doesn't carry repos — the durable
		// source is the registry row, and any caller hitting this
		// path is in degraded mode anyway. Default to empty so the
		// wire shape stays consistent with infoFromRecord.
		Repos:        []string{},
		Capabilities: annotationStringList(pod.Annotations, capabilitiesAnnotation),
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

// podStatus was deleted in tank-operator#83. Status is sourced from
// the sessions.status row column (docs/session-list-redesign.md
// Phase 2), populated by sessioncontroller.RowWriter. The
// scripts/check-removed-chat-runtime.mjs guard blocks
// reintroduction of any live pod-status computation here.

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

func annotationStringList(annotations map[string]string, key string) []string {
	if annotations == nil || annotations[key] == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(annotations[key]), &out); err != nil {
		return []string{}
	}
	return out
}
