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
	SetTestState(ctx context.Context, email, sessionID string, state map[string]any) error
	SetRolloutState(ctx context.Context, email, sessionID string, state map[string]any) error
	SetCloneState(ctx context.Context, email, sessionID string, state map[string]any) error
	Reorder(ctx context.Context, email string, orderedIDs []string) ([]string, error)
	MarkDeleted(ctx context.Context, email, sessionID string) error
}

type sessionRegistryGetter interface {
	Get(ctx context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error)
}

// RowEmitter publishes the current state of one sessions row on the
// per-(owner, scope) NATS row-update subject. After
// docs/session-list-redesign.md Phase 3 every Manager mutation calls
// PublishCurrentRow once the durable write has committed; the SPA's
// SessionStore is a row cache that replaces-by-id from the row-update
// stream. Satisfied by *sessioncontroller.RowPublisher.
type RowEmitter interface {
	PublishCurrentRow(ctx context.Context, owner, sessionID string)
}

// Manager owns session lifecycle: create, delete, patch, reaper.
type Manager struct {
	client    kubernetes.Interface
	restCfg   *rest.Config
	namespace string
	registry  SessionRegistry
	emitter   RowEmitter
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
	geminiAPIProxyIP string

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
	GeminiAPIProxyHost string
}

func NewManager(client kubernetes.Interface, restCfg *rest.Config, namespace string, registry SessionRegistry, emitter RowEmitter, opts ManagerOptions) *Manager {
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
		emitter:        emitter,
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
	if opts.GeminiAPIProxyHost != "" {
		m.geminiAPIProxyIP = resolveIP(opts.GeminiAPIProxyHost)
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
		m.publishRow(ctx, owner, sessionID)
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

// CreateOptions packages the inputs to a session-create call. Replaces
// the prior positional `(owner, mode, glimmungContext, requestedAt)`
// list now that `Repos` and any future per-create knob would push the
// arity past readable. Per docs/quality-timeframes.md "settled
// contracts over compatibility layers": this is the only Create
// signature — handlers, internal-API callers, and tests all use it.
type CreateOptions struct {
	// Owner is the human email that owns the new session. Required.
	Owner string
	// Mode is the session shape (claude_gui, codex_cli, hermes_gui,
	// …). Empty defaults to DefaultSessionMode via NormalizeSessionMode.
	Mode string
	// GlimmungContext is the optional opaque map serialized into the
	// pod's TANK_GLIMMUNG_CONTEXT_JSON env var. nil for the standard
	// human-create path; populated by handleCreateSessionWithContext
	// when a Glimmung run hands off into a fresh session.
	GlimmungContext map[string]any
	// RequestedAt is an externally-supplied creation timestamp; empty
	// defaults to now. Used by the service-principal handoff path so
	// the registry row's requested_at matches the upstream
	// Glimmung run, not the orchestrator's clock.
	RequestedAt string
	// Repos is the durable "owner/name" slug selection from the splash
	// page. Empty slice (or nil) means "no auto-clone." The slugs are
	// validated at the handler boundary; manager.Create stores them on
	// the registry row and threads them into the pod manifest for the
	// repo-cloner init container.
	Repos []string
	// Name is the optional display title supplied by the workspace title
	// bar before the create request is sent. It is normalized once here
	// and becomes part of the initial durable sessions row.
	Name *string
	// Model/Effort are the session-owned SDK run configuration. The
	// HTTP handler validates provider-specific effort values before
	// calling Create; Manager persists them unchanged.
	Model  string
	Effort string
}

// Create creates a new session pod and registers it in the registry.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (Info, error) {
	owner := opts.Owner
	mode := sessionmodel.NormalizeSessionMode(opts.Mode)
	if !sessionmodel.IsSessionMode(mode) {
		return Info{}, fmt.Errorf("unknown session mode: %q", mode)
	}
	requestedAt := opts.RequestedAt
	if requestedAt == "" {
		requestedAt = nowISO()
	}
	glimmungContext := opts.GlimmungContext
	repos := opts.Repos
	if repos == nil {
		repos = []string{}
	}
	model := opts.Model
	effort := opts.Effort
	name := sessionmodel.NormalizeName(opts.Name)

	// hermes_gui (and any future no-pod mode) short-circuits the K8s
	// pod-create path. The session exists as a Postgres registry row +
	// an entry in the SSE stream; turns are routed to an external
	// backend by handleSubmitTurn. The rest of the orchestrator
	// (snapshot, SSE, conversation_read_state, …) treats it like any
	// other session because nothing else assumes a pod object exists
	// except the explicitly pod-aware helpers (findPodBySessionID,
	// resolveSessionPod) — which short-circuit on the same predicate.
	// See nelsong6/tank-operator#540.
	if sessionmodel.IsNoPodMode(mode) {
		// No-pod modes don't have a /workspace to clone into;
		// the handler boundary rejects repos when the mode is
		// no-pod. The repos arg is threaded for forward-compat with
		// any future no-pod mode that wants repo metadata visible in
		// the SPA without pod-side cloning.
		return m.createNoPodSession(ctx, owner, mode, requestedAt, repos, name, model, effort)
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
	if m.geminiAPIProxyIP == "" {
		m.geminiAPIProxyIP = resolveIP(os.Getenv("GEMINI_API_PROXY_HOST"))
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

	manifestOpts := m.manifestOpts
	manifestOpts.OAuthGatewayIP = m.oauthGatewayIP
	manifestOpts.APIProxyIP = m.apiProxyIP
	manifestOpts.CodexAPIProxyIP = m.codexAPIProxyIP
	manifestOpts.GeminiAPIProxyIP = m.geminiAPIProxyIP
	manifestOpts.GlimmungContextJSON = contextJSON
	manifestOpts.Repos = repos
	manifestOpts.Name = name

	manifest := sessionmodel.PodManifest(sessionID, owner, mode, manifestOpts)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return Info{}, err
	}

	var pod corev1.Pod
	if err := json.Unmarshal(raw, &pod); err != nil {
		return Info{}, fmt.Errorf("manifest unmarshal: %w", err)
	}

	// Phase 2 write-order inversion (docs/session-list-redesign.md):
	// registry row goes in BEFORE the K8s pod create. The pre-Phase-2
	// order was pod-create-first, registry-second, which left a brief
	// race window where Reader.List would see a pod without a registry
	// row and fall through to the pod-fallback path — the path that
	// resurrected just-deleted sessions during the ~75s pod-termination
	// window. Reader.List no longer reads pods at all, so this window
	// becomes "session created but not yet visible to the snapshot,"
	// which is fine: the POST response carries the Info directly to
	// the SPA which adds it optimistically; the next snapshot finds it.
	//
	// On pod-create failure after the registry write succeeds, we mark
	// the row visible=false so the snapshot stops returning it.
	podName := "session-" + sessionID
	assignment, reserved, err := m.reserveSessionAvatars(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if reserved && assignment.AgentAvatarID == "" {
		return Info{}, fmt.Errorf("reserve session avatars: no agent avatars available")
	}
	if m.registry != nil {
		if regErr := m.registry.Upsert(ctx, sessionmodel.SessionRecord{
			ID:             sessionID,
			Email:          owner,
			Mode:           mode,
			Scope:          m.manifestOpts.SessionScope,
			PodName:        podName,
			Visible:        true,
			Name:           name,
			RequestedAt:    requestedAt,
			UpdatedAt:      requestedAt,
			Repos:          repos,
			Model:          model,
			Effort:         effort,
			AgentAvatarID:  assignment.AgentAvatarID,
			SystemAvatarID: assignment.SystemAvatarID,
		}); regErr != nil {
			slog.Warn("create registry upsert failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}

	created, err := m.client.CoreV1().Pods(m.namespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		if m.registry != nil {
			if delErr := m.registry.MarkDeleted(ctx, owner, sessionID); delErr != nil {
				slog.Warn("create rollback: registry mark-deleted failed",
					"session_id", sessionID, "owner", owner, "error", delErr)
			}
		}
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

	info := Info{
		ID:             sessionID,
		PodName:        &podName,
		Owner:          owner,
		Status:         "Pending",
		Mode:           mode,
		RequestedAt:    &requestedAt,
		CreatedAt:      createdAt,
		Name:           name,
		Repos:          repos,
		Model:          model,
		Effort:         effort,
		AgentAvatarID:  assignment.AgentAvatarID,
		SystemAvatarID: assignment.SystemAvatarID,
	}

	// Refresh the registry row with the K8s-assigned created_at so the
	// snapshot's CreatedAt matches the pod object's creation timestamp.
	if m.registry != nil && createdAt != nil {
		if regErr := m.registry.Upsert(ctx, sessionmodel.SessionRecord{
			ID:             sessionID,
			Email:          owner,
			Mode:           mode,
			Scope:          m.manifestOpts.SessionScope,
			PodName:        podName,
			Visible:        true,
			Name:           name,
			RequestedAt:    requestedAt,
			CreatedAt:      *createdAt,
			UpdatedAt:      requestedAt,
			Repos:          repos,
			Model:          model,
			Effort:         effort,
			AgentAvatarID:  assignment.AgentAvatarID,
			SystemAvatarID: assignment.SystemAvatarID,
		}); regErr != nil {
			slog.Warn("create registry created_at refresh failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}

	if !reserved && (assignment.AgentAvatarID == "" || assignment.SystemAvatarID == "") {
		assignment = m.assignSessionAvatars(ctx, owner, sessionID)
		info.AgentAvatarID = assignment.AgentAvatarID
		info.SystemAvatarID = assignment.SystemAvatarID
	}

	m.publishRow(ctx, owner, sessionID)
	return info, nil
}

// Delete deletes a session pod (if any) and marks it deleted in the registry.
// For no-pod modes (hermes_gui) there is no pod object; the registry mark
// and reaper bookkeeping happen identically.
func (m *Manager) Delete(ctx context.Context, owner, sessionID string) error {
	// Pod lookup is best-effort and only meaningful for pod-backed modes.
	// For no-pod sessions, findPodBySessionID returns ErrNotFound, which we
	// pass through to the registry mark below.
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
	m.publishRow(ctx, owner, sessionID)
	return nil
}

// createNoPodSession allocates a session_registry row for a no-pod mode
// (today: hermes_gui) and marks it Ready immediately. There is no pod to
// reach Ready against, so the snapshot status is whatever steady-state
// makes sense for the external backend — here, "Active" once the row is
// written. Reaper does not touch no-pod sessions because reaper lists
// pods, not registry rows.
func (m *Manager) createNoPodSession(ctx context.Context, owner, mode, requestedAt string, repos []string, name *string, model, effort string) (Info, error) {
	sessionID, err := m.nextSessionID(ctx)
	if err != nil {
		return Info{}, err
	}

	// PodName empty is the signal to downstream pod-aware code paths that
	// this is a no-pod session. findPodBySessionID returns ErrNotFound,
	// resolveSessionPod returns 4xx, handleSubmitTurn checks the mode and
	// routes to the external backend bridge instead of NATS.
	now := nowISO()
	assignment, reserved, err := m.reserveSessionAvatars(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if reserved && assignment.AgentAvatarID == "" {
		return Info{}, fmt.Errorf("reserve no-pod session avatars: no agent avatars available")
	}
	rec := sessionmodel.SessionRecord{
		ID:             sessionID,
		Email:          owner,
		Mode:           mode,
		Scope:          m.manifestOpts.SessionScope,
		PodName:        "",
		Visible:        true,
		Name:           name,
		RequestedAt:    requestedAt,
		CreatedAt:      now,
		UpdatedAt:      now,
		Status:         "Active",
		ReadyAt:        now,
		Repos:          repos,
		Model:          model,
		Effort:         effort,
		AgentAvatarID:  assignment.AgentAvatarID,
		SystemAvatarID: assignment.SystemAvatarID,
	}
	if m.registry != nil {
		if regErr := m.registry.Upsert(ctx, rec); regErr != nil {
			return Info{}, fmt.Errorf("no-pod session registry upsert: %w", regErr)
		}
	}
	if !reserved && (assignment.AgentAvatarID == "" || assignment.SystemAvatarID == "") {
		assignment = m.assignSessionAvatars(ctx, owner, sessionID)
	}

	m.mu.Lock()
	m.lastActivity[sessionID] = time.Now()
	m.wsCount[sessionID] = 0
	m.mu.Unlock()

	info := Info{
		ID:             sessionID,
		PodName:        nil,
		Owner:          owner,
		Status:         "Active",
		Mode:           mode,
		RequestedAt:    &requestedAt,
		CreatedAt:      &now,
		ReadyAt:        &now,
		Name:           name,
		Repos:          repos,
		Model:          model,
		Effort:         effort,
		AgentAvatarID:  assignment.AgentAvatarID,
		SystemAvatarID: assignment.SystemAvatarID,
	}
	m.publishRow(ctx, owner, sessionID)
	return info, nil
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
	m.publishRow(ctx, owner, sessionID)

	if registered, err := m.GetRegisteredByOwner(ctx, owner, sessionID); err == nil {
		return registered, nil
	}
	return m.GetByOwner(ctx, owner, sessionID)
}

// SetTestState updates the row's test_state column AND patches the
// matching pod annotation (the session-agent reads the annotation via
// the projected downward-API volume). Both writes are load-bearing
// in steady state: the column is the snapshot-facing replica; the
// annotation is what the agent code path consults.
func (m *Manager) SetTestState(ctx context.Context, owner, sessionID string, active bool, slotIndex *int, url *string) (Info, error) {
	state := map[string]any{"active": active}
	if slotIndex != nil {
		state["slot_index"] = *slotIndex
	}
	if url != nil && *url != "" {
		state["url"] = *url
	}
	raw, _ := json.Marshal(state)
	annotations := map[string]string{testStateAnnotation: string(raw)}
	if active {
		annotations[rolloutStateAnnotation] = `{"active":false}`
	}
	return m.patchStateAnnotations(ctx, owner, sessionID,
		annotations,
		func(c context.Context) error {
			if m.registry == nil {
				return nil
			}
			return m.registry.SetTestState(c, owner, sessionID, state)
		})
}

// SetRolloutState updates the row's rollout_state column AND patches
// the matching pod annotation. Same shape as SetTestState.
func (m *Manager) SetRolloutState(ctx context.Context, owner, sessionID string, active bool) (Info, error) {
	state := map[string]any{"active": active}
	raw, _ := json.Marshal(state)
	annotations := map[string]string{rolloutStateAnnotation: string(raw)}
	if active {
		annotations[testStateAnnotation] = `{"active":false}`
	}
	return m.patchStateAnnotations(ctx, owner, sessionID,
		annotations,
		func(c context.Context) error {
			if m.registry == nil {
				return nil
			}
			return m.registry.SetRolloutState(c, owner, sessionID, state)
		})
}

// SetCloneState replaces the sessions.clone_state payload written by
// the repo-cloner init container and publishes the updated row to the
// sidebar stream. It does not patch pod annotations: clone_state is a
// durable UI/reporting surface, not runtime input for a live container.
func (m *Manager) SetCloneState(ctx context.Context, owner, sessionID string, state map[string]any) (Info, error) {
	if m.registry != nil {
		if err := m.registry.SetCloneState(ctx, owner, sessionID, state); err != nil {
			return Info{}, err
		}
	}
	m.publishRow(ctx, owner, sessionID)
	return m.GetByOwner(ctx, owner, sessionID)
}

type runtimeConfigRegistry interface {
	SetRuntimeConfig(ctx context.Context, email, sessionID, model, effort string) error
}

type sessionAvatarReserver interface {
	ReserveSessionAvatars(ctx context.Context, owner, sessionID string) (sessionmodel.SessionAvatarAssignment, error)
}

type sessionAvatarAssigner interface {
	AssignSessionAvatars(ctx context.Context, owner, sessionID string) (sessionmodel.SessionAvatarAssignment, error)
}

func (m *Manager) reserveSessionAvatars(ctx context.Context, owner, sessionID string) (sessionmodel.SessionAvatarAssignment, bool, error) {
	reserver, ok := m.registry.(sessionAvatarReserver)
	if !ok {
		return sessionmodel.SessionAvatarAssignment{}, false, nil
	}
	assignment, err := reserver.ReserveSessionAvatars(ctx, owner, sessionID)
	if err != nil {
		slog.Warn("session avatar reservation failed",
			"session_id", sessionID, "owner", owner, "error", err)
		return sessionmodel.SessionAvatarAssignment{}, true, fmt.Errorf("reserve session avatars: %w", err)
	}
	return assignment, true, nil
}

func (m *Manager) assignSessionAvatars(ctx context.Context, owner, sessionID string) sessionmodel.SessionAvatarAssignment {
	assigner, ok := m.registry.(sessionAvatarAssigner)
	if !ok {
		return sessionmodel.SessionAvatarAssignment{}
	}
	assignment, err := assigner.AssignSessionAvatars(ctx, owner, sessionID)
	if err != nil {
		slog.Warn("session avatar assignment failed",
			"session_id", sessionID, "owner", owner, "error", err)
		return sessionmodel.SessionAvatarAssignment{}
	}
	return assignment
}

// SetRuntimeConfig records the model/effort the runner actually applied
// to the provider executable/SDK and publishes the updated session row.
func (m *Manager) SetRuntimeConfig(ctx context.Context, owner, sessionID, model, effort string) (Info, error) {
	registry, ok := m.registry.(runtimeConfigRegistry)
	if !ok {
		return Info{}, ErrNotFound
	}
	if err := registry.SetRuntimeConfig(ctx, owner, sessionID, model, effort); err != nil {
		return Info{}, err
	}
	m.publishRow(ctx, owner, sessionID)
	return m.GetRegisteredByOwner(ctx, owner, sessionID)
}

// ReorderSessions persists the complete visible sidebar order for one
// owner and publishes the updated rows so every connected browser tab
// converges on the same durable order.
func (m *Manager) ReorderSessions(ctx context.Context, owner string, orderedIDs []string) error {
	if m.registry == nil {
		return nil
	}
	publishIDs, err := m.registry.Reorder(ctx, owner, orderedIDs)
	if err != nil {
		return err
	}
	for _, id := range publishIDs {
		m.publishRow(ctx, owner, id)
	}
	return nil
}

func (m *Manager) patchStateAnnotations(
	ctx context.Context,
	owner, sessionID string,
	annotations map[string]string,
	writeColumn func(context.Context) error,
) (Info, error) {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": annotations,
		},
	}
	raw, _ := json.Marshal(patch)
	pod, err := m.findPodBySessionID(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if _, patchErr := m.client.CoreV1().Pods(m.namespace).Patch(ctx, pod.Name, types.MergePatchType, raw, metav1.PatchOptions{}); patchErr != nil && !k8serrors.IsNotFound(patchErr) {
		return Info{}, fmt.Errorf("patch state annotations: %w", patchErr)
	}
	if writeColumn != nil {
		if err := writeColumn(ctx); err != nil {
			slog.Warn("session-state column write failed",
				"session_id", sessionID, "owner", owner,
				"annotations", annotations, "error", err)
		}
	}
	if m.emitter != nil {
		m.emitter.PublishCurrentRow(ctx, owner, sessionID)
	}
	return m.GetByOwner(ctx, owner, sessionID)
}

// GetByOwner retrieves a session and validates ownership.
func (m *Manager) GetByOwner(ctx context.Context, owner, sessionID string) (Info, error) {
	info, err := m.reader().Get(ctx, owner, sessionID)
	if errors.Is(err, ErrNotFound) {
		registered, regErr := m.GetRegisteredByOwner(ctx, owner, sessionID)
		if regErr == nil {
			if sessionmodel.IsNoPodMode(registered.Mode) {
				return registered, nil
			}
			return Info{}, err
		}
		if !errors.Is(regErr, ErrNotFound) {
			return Info{}, regErr
		}
	}
	return info, err
}

// GetByID retrieves a session by ID without verifying ownership. The
// returned Info carries the resolved owner so the caller can authorize.
// Read-only paths use this for admin cross-user reads; writes continue
// to use GetByOwner so an admin token can't accidentally mutate
// someone else's session. See backend-go/internal/sessions/sessions.go.
func (m *Manager) GetByID(ctx context.Context, sessionID string) (Info, error) {
	info, err := m.reader().GetByID(ctx, sessionID)
	return info, err
}

// GetRegisteredByOwner retrieves a durable session row for read-only paths
// that do not require a live pod. Transcript links must continue to resolve
// after the session pod exits; the registry row is the cold-open authority.
func (m *Manager) GetRegisteredByOwner(ctx context.Context, owner, sessionID string) (Info, error) {
	getter, ok := m.registry.(sessionRegistryGetter)
	if !ok {
		return Info{}, ErrNotFound
	}
	record, found, err := getter.Get(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if !found || !record.Visible {
		return Info{}, ErrNotFound
	}
	return infoFromRecord(owner, record), nil
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
	return NewReaderFull(m.client, m.namespace, reg, m.scope)
}

// registryAdapter wraps the write-capable SessionRegistry into a read-only Registry.
type registryAdapter struct{ r SessionRegistry }

func (a *registryAdapter) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	return a.r.List(ctx, owner)
}

// publishRow is the Manager-side bridge from a user-action mutation
// to the per-owner row-update wire. Failures are logged inside the
// emitter; the registry write is the source of truth so a missed
// publish is recoverable on the SPA's next SSE reconnect (catch-up
// reads the sessions table directly).
func (m *Manager) publishRow(ctx context.Context, owner, sessionID string) {
	if m.emitter == nil || owner == "" || sessionID == "" {
		return
	}
	m.emitter.PublishCurrentRow(ctx, owner, sessionID)
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
