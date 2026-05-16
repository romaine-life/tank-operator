package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

const (
	defaultIdleTimeout    = 5 * time.Minute
	defaultReaperInterval = 60 * time.Second
	podReadyTimeout       = 90 * time.Second
)

// SessionRegistry is a write-capable registry interface.
type SessionRegistry interface {
	List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error)
	NextSessionID(ctx context.Context) (string, error)
	Upsert(ctx context.Context, record sessionmodel.SessionRecord) error
	SetName(ctx context.Context, email, sessionID string, name *string) error
	MarkDeleted(ctx context.Context, email, sessionID string) error
}

// SessionListEventPublisher is implemented by the session bus; it
// publishes one typed lifecycle-event payload on the per-owner NATS
// session-list events subject. Replaces the prior SessionListWaker
// (opaque wake-and-refetch) per tank-operator#83 — the SSE handler now
// forwards the payload verbatim to clients instead of just telling them
// to re-fetch.
type SessionListEventPublisher interface {
	PublishSessionListEvent(ctx context.Context, email string, payload []byte) error
}

// LifecycleAppender is the durable side of the producer pipeline.
// Manager mutations append a typed row here before publishing on the bus
// so the SSE cursor-resume path on reconnect catches up from Postgres
// even if the NATS publish was lost. Satisfied by lifecycleevents.Store.
type LifecycleAppender interface {
	Append(ctx context.Context, event lifecycleevents.Event) (lifecycleevents.Event, bool, error)
}

// Manager owns session lifecycle: create, delete, patch, reaper.
type Manager struct {
	client    kubernetes.Interface
	restCfg   *rest.Config
	namespace string
	registry  SessionRegistry
	lifecycle LifecycleAppender
	publisher SessionListEventPublisher
	scope     string

	manifestOpts sessionmodel.ManifestOptions

	// In-memory activity tracking for reaper (single replica only).
	mu           sync.Mutex
	wsCount      map[string]int
	lastActivity map[string]time.Time

	idleTimeout    time.Duration
	reaperInterval time.Duration

	// Resolved ClusterIPs for host-alias injection.
	oauthGatewayIP  string
	apiProxyIP      string
	codexAPIProxyIP string

	localCounter     int64
	localCounterLock sync.Mutex
}

// ManagerOptions configures a new Manager.
type ManagerOptions struct {
	ManifestOpts      sessionmodel.ManifestOptions
	IdleTimeout       time.Duration
	ReaperInterval    time.Duration
	OAuthGatewayHost  string
	APIProxyHost      string
	CodexAPIProxyHost string
}

func NewManager(client kubernetes.Interface, restCfg *rest.Config, namespace string, registry SessionRegistry, lifecycle LifecycleAppender, publisher SessionListEventPublisher, opts ManagerOptions) *Manager {
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = defaultIdleTimeout
		if v := os.Getenv("IDLE_TIMEOUT_SECONDS"); v != "" {
			var n int
			fmt.Sscan(v, &n)
			if n > 0 {
				opts.IdleTimeout = time.Duration(n) * time.Second
			}
		}
	}
	if opts.ReaperInterval == 0 {
		opts.ReaperInterval = defaultReaperInterval
	}
	if opts.ManifestOpts.SessionsNamespace == "" {
		opts.ManifestOpts.SessionsNamespace = namespace
	}
	if opts.ManifestOpts.SessionScope == "" {
		opts.ManifestOpts.SessionScope = "default"
	}
	m := &Manager{
		client:         client,
		restCfg:        restCfg,
		namespace:      namespace,
		registry:       registry,
		lifecycle:      lifecycle,
		publisher:      publisher,
		scope:          opts.ManifestOpts.SessionScope,
		manifestOpts:   opts.ManifestOpts,
		wsCount:        map[string]int{},
		lastActivity:   map[string]time.Time{},
		idleTimeout:    opts.IdleTimeout,
		reaperInterval: opts.ReaperInterval,
	}
	if opts.OAuthGatewayHost != "" {
		m.oauthGatewayIP = resolveIP(opts.OAuthGatewayHost)
	}
	if opts.APIProxyHost != "" {
		m.apiProxyIP = resolveIP(opts.APIProxyHost)
	}
	if opts.CodexAPIProxyHost != "" {
		m.codexAPIProxyIP = resolveIP(opts.CodexAPIProxyHost)
	}
	return m
}

func resolveIP(host string) string {
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		slog.Warn("could not resolve service IP", "host", host, "err", err)
		return ""
	}
	return addrs[0]
}

// StartReaper launches the idle session reaper in a background goroutine.
func (m *Manager) StartReaper(ctx context.Context) {
	go m.reaperLoop(ctx)
}

func (m *Manager) reaperLoop(ctx context.Context) {
	ticker := time.NewTicker(m.reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reapIdle(ctx)
		}
	}
}

func (m *Manager) reapIdle(ctx context.Context) {
	pods, err := m.client.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=tank-operator",
	})
	if err != nil {
		return
	}
	now := time.Now()
	for _, pod := range pods.Items {
		sessionID := sessionIDFromPod(&pod)
		if sessionID == "" {
			continue
		}
		owner := pod.Annotations["tank-operator/owner-email"]

		m.mu.Lock()
		wsCount := m.wsCount[sessionID]
		lastAct, hasActivity := m.lastActivity[sessionID]
		if !hasActivity {
			// Adopt with current time so new sessions survive a full idle window.
			m.lastActivity[sessionID] = now
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()

		if wsCount > 0 {
			continue
		}
		if now.Sub(lastAct) < m.idleTimeout {
			continue
		}

		slog.Info("reaping idle session", "session_id", sessionID, "owner", owner, "idle", now.Sub(lastAct).Round(time.Second))
		if err := m.client.CoreV1().Pods(m.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			slog.Warn("reaper delete failed", "pod", pod.Name, "err", err)
			continue
		}
		m.mu.Lock()
		delete(m.wsCount, sessionID)
		delete(m.lastActivity, sessionID)
		m.mu.Unlock()
		if m.registry != nil && owner != "" {
			if regErr := m.registry.MarkDeleted(ctx, owner, sessionID); regErr != nil {
				slog.Warn("reaper registry mark-deleted failed",
					"session_id", sessionID, "owner", owner, "error", regErr)
			}
		}
		m.emitLifecycle(ctx, lifecycleevents.Event{
			Email:        owner,
			SessionScope: m.scope,
			SessionID:    sessionID,
			Type:         lifecycleevents.EventTypeDeleted,
			EventID:      "deleted",
			Payload: map[string]any{
				"pod_name": pod.Name,
				"source":   "reaper",
			},
		})
	}
}

// TrackWS increments the WS connection count and returns a function to decrement.
func (m *Manager) TrackWS(sessionID string) func() {
	m.mu.Lock()
	m.wsCount[sessionID]++
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.wsCount[sessionID] > 0 {
			m.wsCount[sessionID]--
		}
		m.lastActivity[sessionID] = time.Now()
		m.mu.Unlock()
	}
}

// Touch updates the last activity timestamp.
func (m *Manager) Touch(sessionID string) {
	m.mu.Lock()
	m.lastActivity[sessionID] = time.Now()
	m.mu.Unlock()
}

// Create creates a new session pod and registers it in the registry.
func (m *Manager) Create(ctx context.Context, owner, mode string, glimmungContext map[string]any, requestedAt string) (Info, error) {
	mode = sessionmodel.NormalizeSessionMode(mode)
	if !sessionmodel.IsSessionMode(mode) {
		return Info{}, fmt.Errorf("unknown session mode: %q", mode)
	}
	if requestedAt == "" {
		requestedAt = nowISO()
	}

	// Lazy re-resolution for first-install ordering.
	if m.oauthGatewayIP == "" {
		m.oauthGatewayIP = resolveIP(os.Getenv("CLAUDE_OAUTH_GATEWAY_HOST"))
	}
	if m.apiProxyIP == "" {
		m.apiProxyIP = resolveIP(os.Getenv("CLAUDE_API_PROXY_HOST"))
	}
	if m.codexAPIProxyIP == "" {
		m.codexAPIProxyIP = resolveIP(os.Getenv("CODEX_API_PROXY_HOST"))
	}

	sessionID, err := m.nextSessionID(ctx)
	if err != nil {
		return Info{}, err
	}

	contextJSON := ""
	if glimmungContext != nil {
		b, _ := json.Marshal(glimmungContext)
		contextJSON = string(b)
	}

	opts := m.manifestOpts
	opts.OAuthGatewayIP = m.oauthGatewayIP
	opts.APIProxyIP = m.apiProxyIP
	opts.CodexAPIProxyIP = m.codexAPIProxyIP
	opts.GlimmungContextJSON = contextJSON

	manifest := sessionmodel.PodManifest(sessionID, owner, mode, opts)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return Info{}, err
	}

	var pod corev1.Pod
	if err := json.Unmarshal(raw, &pod); err != nil {
		return Info{}, fmt.Errorf("manifest unmarshal: %w", err)
	}

	created, err := m.client.CoreV1().Pods(m.namespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		return Info{}, fmt.Errorf("create pod: %w", err)
	}

	m.mu.Lock()
	m.lastActivity[sessionID] = time.Now()
	m.wsCount[sessionID] = 0
	m.mu.Unlock()

	var createdAt *string
	if !created.CreationTimestamp.IsZero() {
		s := created.CreationTimestamp.UTC().Format("2006-01-02T15:04:05+00:00")
		createdAt = &s
	}
	podName := created.Name

	info := Info{
		ID:          sessionID,
		PodName:     &podName,
		Owner:       owner,
		Status:      "Pending",
		Mode:        mode,
		RequestedAt: &requestedAt,
		CreatedAt:   createdAt,
	}

	if m.registry != nil {
		if regErr := m.registry.Upsert(ctx, sessionmodel.SessionRecord{
			ID:          sessionID,
			Email:       owner,
			Mode:        mode,
			Scope:       m.manifestOpts.SessionScope,
			PodName:     podName,
			Visible:     true,
			RequestedAt: requestedAt,
			CreatedAt:   orEmpty(createdAt),
			UpdatedAt:   requestedAt,
		}); regErr != nil {
			slog.Warn("create registry upsert failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}

	m.emitLifecycle(ctx, lifecycleevents.Event{
		Email:        owner,
		SessionScope: m.scope,
		SessionID:    sessionID,
		Type:         lifecycleevents.EventTypeCreated,
		EventID:      "created",
		Payload: map[string]any{
			"mode":         mode,
			"pod_name":     podName,
			"requested_at": requestedAt,
			"created_at":   orEmpty(createdAt),
		},
	})
	return info, nil
}

// Delete deletes a session pod and marks it deleted in the registry.
func (m *Manager) Delete(ctx context.Context, owner, sessionID string) error {
	pod, err := m.findPodBySessionID(ctx, owner, sessionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if pod != nil {
		if delErr := m.client.CoreV1().Pods(m.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
			return fmt.Errorf("delete pod: %w", delErr)
		}
	}

	m.mu.Lock()
	delete(m.wsCount, sessionID)
	delete(m.lastActivity, sessionID)
	m.mu.Unlock()

	if m.registry != nil {
		if regErr := m.registry.MarkDeleted(ctx, owner, sessionID); regErr != nil {
			slog.Warn("delete registry mark-deleted failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}
	deletedPayload := map[string]any{"source": "user"}
	if pod != nil {
		deletedPayload["pod_name"] = pod.Name
	}
	m.emitLifecycle(ctx, lifecycleevents.Event{
		Email:        owner,
		SessionScope: m.scope,
		SessionID:    sessionID,
		Type:         lifecycleevents.EventTypeDeleted,
		EventID:      "deleted",
		Payload:      deletedPayload,
	})
	return nil
}

// SetName updates the display name annotation on the pod and registry.
func (m *Manager) SetName(ctx context.Context, owner, sessionID string, name *string) (Info, error) {
	normalized := sessionmodel.NormalizeName(name)
	annotationValue := ""
	if normalized != nil {
		annotationValue = *normalized
	}

	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				"tank-operator/display-name": annotationValue,
			},
		},
	}
	raw, _ := json.Marshal(patch)
	pod, err := m.findPodBySessionID(ctx, owner, sessionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Info{}, err
	}
	if pod != nil {
		if _, patchErr := m.client.CoreV1().Pods(m.namespace).Patch(ctx, pod.Name, types.MergePatchType, raw, metav1.PatchOptions{}); patchErr != nil && !k8serrors.IsNotFound(patchErr) {
			return Info{}, fmt.Errorf("patch pod name: %w", patchErr)
		}
	}

	if m.registry != nil {
		if regErr := m.registry.SetName(ctx, owner, sessionID, normalized); regErr != nil {
			slog.Warn("set-name registry update failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}
	namePayload := map[string]any{"name": annotationValue}
	m.emitLifecycle(ctx, lifecycleevents.Event{
		Email:        owner,
		SessionScope: m.scope,
		SessionID:    sessionID,
		Type:         lifecycleevents.EventTypeNameChanged,
		EventID:      fmt.Sprintf("name_changed:%d", time.Now().UnixNano()),
		Payload:      namePayload,
	})

	return m.GetByOwner(ctx, owner, sessionID)
}

// SetTestState updates the test-state annotation on the pod.
func (m *Manager) SetTestState(ctx context.Context, owner, sessionID string, active bool, slotIndex *int, url *string) (Info, error) {
	state := map[string]any{"active": active}
	if slotIndex != nil {
		state["slot_index"] = *slotIndex
	}
	if url != nil && *url != "" {
		state["url"] = *url
	}
	raw, _ := json.Marshal(state)
	return m.patchAnnotation(ctx, owner, sessionID,
		"tank-operator/test-state", string(raw),
		lifecycleevents.EventTypeTestStateChanged, "test_state_changed", state)
}

// SetRolloutState updates the rollout-state annotation on the pod.
func (m *Manager) SetRolloutState(ctx context.Context, owner, sessionID string, active bool) (Info, error) {
	state := map[string]any{"active": active}
	raw, _ := json.Marshal(state)
	return m.patchAnnotation(ctx, owner, sessionID,
		"tank-operator/rollout-state", string(raw),
		lifecycleevents.EventTypeRolloutStateChanged, "rollout_state_changed", state)
}

func (m *Manager) patchAnnotation(
	ctx context.Context,
	owner, sessionID, annotation, value string,
	eventType, eventIDPrefix string,
	payload map[string]any,
) (Info, error) {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{annotation: value},
		},
	}
	raw, _ := json.Marshal(patch)
	pod, err := m.findPodBySessionID(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if _, patchErr := m.client.CoreV1().Pods(m.namespace).Patch(ctx, pod.Name, types.MergePatchType, raw, metav1.PatchOptions{}); patchErr != nil && !k8serrors.IsNotFound(patchErr) {
		return Info{}, fmt.Errorf("patch annotation %s: %w", annotation, patchErr)
	}
	m.emitLifecycle(ctx, lifecycleevents.Event{
		Email:        owner,
		SessionScope: m.scope,
		SessionID:    sessionID,
		Type:         eventType,
		EventID:      fmt.Sprintf("%s:%d", eventIDPrefix, time.Now().UnixNano()),
		Payload:      payload,
	})
	return m.GetByOwner(ctx, owner, sessionID)
}

// GetByOwner retrieves a session and validates ownership.
func (m *Manager) GetByOwner(ctx context.Context, owner, sessionID string) (Info, error) {
	info, err := m.reader().Get(ctx, owner, sessionID)
	return info, err
}

// GetPodName waits up to 90s for the session pod to be ready and returns its name.
func (m *Manager) GetPodName(ctx context.Context, owner, sessionID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, podReadyTimeout)
	defer cancel()
	for {
		pod, err := m.findPodBySessionID(ctx, owner, sessionID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				select {
				case <-ctx.Done():
					return "", ErrNotFound
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}
			return "", err
		}
		if podReady(pod) {
			return pod.Name, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("pod not ready: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// GetTerminalEndpoint waits for the pod to be ready and returns (podIP, sandboxAgentPort).
func (m *Manager) GetTerminalEndpoint(ctx context.Context, owner, sessionID string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, podReadyTimeout)
	defer cancel()
	for {
		pod, err := m.findPodBySessionID(ctx, owner, sessionID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				select {
				case <-ctx.Done():
					return "", 0, ErrNotFound
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}
			return "", 0, err
		}
		if podReady(pod) && pod.Status.PodIP != "" {
			return pod.Status.PodIP, sessionmodel.SandboxAgentPort, nil
		}
		select {
		case <-ctx.Done():
			return "", 0, fmt.Errorf("pod not ready: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// findPodBySessionID resolves the session pod by label (preferred — handles
// both the current "session-<id>" naming and hash-suffixed names like
// "session-189268a4e4" from earlier orchestrator versions). Falls back to a
// by-name Get for the brief race between pod Create and the label cache
// catching up. Returns ErrNotOwned if the pod exists but belongs to someone
// else, ErrNotFound if no pod for this session_id is in the namespace.
func (m *Manager) findPodBySessionID(ctx context.Context, owner, sessionID string) (*corev1.Pod, error) {
	pods, err := m.client.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "tank-operator/session-id=" + sessionID,
	})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) > 0 {
		pod := &pods.Items[0]
		if pod.Labels["tank-operator/owner"] != sessionmodel.OwnerLabel(owner) {
			return nil, ErrNotOwned
		}
		return pod, nil
	}
	pod, err := m.client.CoreV1().Pods(m.namespace).Get(ctx, "session-"+sessionID, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if pod.Labels["tank-operator/owner"] != sessionmodel.OwnerLabel(owner) {
		return nil, ErrNotOwned
	}
	return pod, nil
}

// FindPodByIP returns the owner email and pod name for a session pod with the given IP.
func (m *Manager) FindPodByIP(ctx context.Context, podIP string) (ownerEmail, podName string, err error) {
	pods, err := m.client.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=tank-operator",
	})
	if err != nil {
		return "", "", err
	}
	for _, pod := range pods.Items {
		if pod.Status.PodIP == podIP {
			email := pod.Annotations["tank-operator/owner-email"]
			return email, pod.Name, nil
		}
	}
	return "", "", fmt.Errorf("no session pod with IP %s", podIP)
}

func (m *Manager) nextSessionID(ctx context.Context) (string, error) {
	if m.registry != nil {
		return m.registry.NextSessionID(ctx)
	}
	m.localCounterLock.Lock()
	defer m.localCounterLock.Unlock()
	m.localCounter++
	return fmt.Sprintf("%d", m.localCounter), nil
}

// ListSessions returns all sessions for an owner.
func (m *Manager) ListSessions(ctx context.Context, owner string) ([]Info, error) {
	return m.reader().List(ctx, owner)
}

func (m *Manager) reader() *Reader {
	var reg Registry
	if m.registry != nil {
		reg = &registryAdapter{m.registry}
	}
	var lifecycleSrc LifecycleStatusSource
	if statusSrc, ok := m.lifecycle.(LifecycleStatusSource); ok {
		lifecycleSrc = statusSrc
	}
	return NewReaderFull(m.client, m.namespace, reg, lifecycleSrc, m.scope)
}

// registryAdapter wraps the write-capable SessionRegistry into a read-only Registry.
type registryAdapter struct{ r SessionRegistry }

func (a *registryAdapter) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	return a.r.List(ctx, owner)
}

// emitLifecycle is the Manager-side bridge from a user-action mutation
// to the per-owner durable lifecycle ledger + NATS event stream. Failures
// are logged but never block the parent mutation: the registry write +
// pod patch are the source of truth; the ledger row drives the sidebar's
// live update but the next SSE reconnect will resync from Postgres
// anyway. Identical event_id collapses with the pod-informer leader's
// row for the same transition via the unique constraint.
func (m *Manager) emitLifecycle(ctx context.Context, event lifecycleevents.Event) {
	if event.Type == "" || event.SessionID == "" || event.Email == "" {
		return
	}
	if m.lifecycle == nil {
		return
	}
	assigned, alreadyExists, err := m.lifecycle.Append(ctx, event)
	if err != nil {
		slog.Warn("manager lifecycle append failed",
			"session_id", event.SessionID,
			"type", event.Type,
			"error", err,
		)
		return
	}
	if alreadyExists || m.publisher == nil {
		return
	}
	payload, err := json.Marshal(assigned)
	if err != nil {
		slog.Warn("manager lifecycle marshal failed",
			"session_id", event.SessionID,
			"type", event.Type,
			"error", err,
		)
		return
	}
	if err := m.publisher.PublishSessionListEvent(ctx, assigned.Email, payload); err != nil {
		slog.Warn("manager lifecycle publish failed",
			"session_id", event.SessionID,
			"type", event.Type,
			"owner", assigned.Email,
			"error", err,
		)
	}
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.999999+00:00")
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
