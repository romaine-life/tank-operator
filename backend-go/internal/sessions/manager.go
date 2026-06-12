package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	podReadyTimeout = 90 * time.Second
)

// defaultSessionScope is the production session scope. Session-image overrides
// are never applied to it (the write path refuses it too); only test slots,
// whose scope is the slot name, are repointable.
const defaultSessionScope = "default"

// SessionImageOverrides resolves a durable, scope-keyed override of the
// container images the orchestrator stamps onto NEW session pods. Implemented
// by the pgstore override table (adapted in cmd/tank-operator). A nil resolver
// disables the feature, which is the production default — prod orchestrators
// never wire one.
type SessionImageOverrides interface {
	// Get returns the override images for a scope. ok=false means "no override
	// set" (the caller falls back to the configured image). A non-nil error
	// means the lookup failed; the caller also falls back rather than failing
	// session creation.
	Get(ctx context.Context, scope string) (claudeImage, codexImage, antigravityImage string, ok bool, err error)
}

// SessionRegistry is a write-capable registry interface.
type SessionRegistry interface {
	List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error)
	NextSessionID(ctx context.Context) (string, error)
	Upsert(ctx context.Context, record sessionmodel.SessionRecord) error
	SetName(ctx context.Context, email, sessionID string, name *string) error
	SetOpenTarget(ctx context.Context, email, sessionID, target string) error
	SetBugLabel(ctx context.Context, email, sessionID string, label *sessionmodel.SessionBugLabel) error
	SetBugLabels(ctx context.Context, email, sessionID string, labels []*sessionmodel.SessionBugLabel) error
	SetTestState(ctx context.Context, email, sessionID string, state map[string]any) error
	SetRolloutState(ctx context.Context, email, sessionID string, state map[string]any) error
	SetCloneState(ctx context.Context, email, sessionID string, state map[string]any) error
	Reorder(ctx context.Context, email string, orderedIDs []string) ([]string, error)
	MarkDeleted(ctx context.Context, email, sessionID string) error
}

type sessionRegistryGetter interface {
	Get(ctx context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error)
}

type sessionRegistryOwnerResolver interface {
	OwnerForSession(ctx context.Context, scope, sessionID string) (string, error)
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

	// imageOverrides repoints NEW session pods at a branch-built session image
	// for this orchestrator's (test-slot) scope. nil in production.
	imageOverrides         SessionImageOverrides
	onImageOverrideApplied func(scope, mode, kind string)

	// Resolved ClusterIPs for host-alias injection.
	oauthGatewayIP        string
	apiProxyIP            string
	codexAPIProxyIP       string
	antigravityAPIProxyIP string

	localCounter     int64
	localCounterLock sync.Mutex
}

// ManagerOptions configures a new Manager.
type ManagerOptions struct {
	ManifestOpts      sessionmodel.ManifestOptions
	OAuthGatewayHost  string
	APIProxyHost      string
	CodexAPIProxyHost string
	// AntigravityAPIProxyHost is the in-cluster antigravity-api-proxy Service
	// (fronts cloudcode-pa.googleapis.com). agy_gui pods host-alias the Google
	// data-plane host to this proxy so the refresh token stays in the proxy.
	AntigravityAPIProxyHost string
	// ImageOverrides, when non-nil, lets the orchestrator repoint NEW session
	// pods at a branch-built session image for its (test-slot) scope. Left nil
	// in production. OnImageOverrideApplied is an optional metrics/log hook
	// invoked when an override is actually stamped onto a pod.
	ImageOverrides         SessionImageOverrides
	OnImageOverrideApplied func(scope, mode, kind string)
}

func NewManager(client kubernetes.Interface, restCfg *rest.Config, namespace string, registry SessionRegistry, emitter RowEmitter, opts ManagerOptions) *Manager {
	if opts.ManifestOpts.SessionsNamespace == "" {
		opts.ManifestOpts.SessionsNamespace = namespace
	}
	if opts.ManifestOpts.SessionScope == "" {
		opts.ManifestOpts.SessionScope = "default"
	}
	m := &Manager{
		client:                 client,
		restCfg:                restCfg,
		namespace:              namespace,
		registry:               registry,
		emitter:                emitter,
		scope:                  opts.ManifestOpts.SessionScope,
		manifestOpts:           opts.ManifestOpts,
		imageOverrides:         opts.ImageOverrides,
		onImageOverrideApplied: opts.OnImageOverrideApplied,
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
	if opts.AntigravityAPIProxyHost != "" {
		m.antigravityAPIProxyIP = resolveIP(opts.AntigravityAPIProxyHost)
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

// applyImageOverride repoints a NEW session pod at a durable, scope-keyed
// session-image override when one is set for this orchestrator's scope. This is
// the test-slot "point the slot at a branch session image" mechanism
// (docs/testing.md): newly-created sessions boot the override image the same way
// production boots its chart-pinned image — no runtime overlay, no fidelity gap.
//
// It is a deliberate no-op when no resolver is wired (production never wires
// one) or for the production scope, so prod always stamps the configured
// SESSION_IMAGE / CODEX_SESSION_IMAGE / ANTIGRAVITY_SESSION_IMAGE. A lookup
// error falls back to the pinned image rather than failing session creation.
func (m *Manager) applyImageOverride(ctx context.Context, opts *sessionmodel.ManifestOptions, mode string) {
	if m.imageOverrides == nil || m.scope == "" || m.scope == defaultSessionScope {
		return
	}
	claudeImage, codexImage, antigravityImage, ok, err := m.imageOverrides.Get(ctx, m.scope)
	if err != nil {
		slog.Warn("session image override lookup failed; using pinned image",
			"scope", m.scope, "mode", mode, "error", err)
		return
	}
	if !ok {
		return
	}
	kind := ""
	if sessionmodel.IsAntigravityMode(mode) {
		if antigravityImage != "" {
			opts.AntigravitySessionImage = antigravityImage
			opts.AntigravitySessionImageMetadata = sessionmodel.ImageVersionMetadata{"source": "test_slot_override"}
			kind = "antigravity"
		}
	} else if sessionmodel.IsCodexMode(mode) {
		if codexImage != "" {
			opts.CodexSessionImage = codexImage
			opts.CodexSessionImageMetadata = sessionmodel.ImageVersionMetadata{"source": "test_slot_override"}
			kind = "codex"
		}
	} else if claudeImage != "" {
		opts.SessionImage = claudeImage
		opts.SessionImageMetadata = sessionmodel.ImageVersionMetadata{"source": "test_slot_override"}
		kind = "claude"
	}
	if kind == "" {
		return
	}
	slog.Info("session image override applied",
		"scope", m.scope, "mode", mode, "image_kind", kind)
	if m.onImageOverrideApplied != nil {
		m.onImageOverrideApplied(m.scope, mode, kind)
	}
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
	// Mode is the session shape (claude_gui, codex_cli, etc.). Empty
	// defaults to DefaultSessionMode via NormalizeSessionMode.
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
	// Capabilities is the normalized per-session capability list. Empty keeps
	// the default pod surface. Capabilities are durable row state and pod
	// manifest input; unsupported modes/configurations are rejected before a
	// row or pod is created.
	Capabilities []string
	// BugLabel is the optional Tank-native bug bucket selected at session
	// creation. It is registry-only state and is persisted before the pod is
	// created so the POST response and first row snapshot agree.
	BugLabel *sessionmodel.SessionBugLabel
	// BugLabels is the plural create-time form. BugLabel remains populated for
	// compatibility with clients and row projections that read one label.
	BugLabels []*sessionmodel.SessionBugLabel
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
	capabilities := opts.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	if sessionmodel.HasSessionCapability(capabilities, sessionmodel.SessionCapabilitySpireLensMCP) {
		if !sessionmodel.SpireLensMCPConfigured(m.manifestOpts) {
			return Info{}, fmt.Errorf("%s capability is not configured for this deployment", sessionmodel.SessionCapabilitySpireLensMCP)
		}
	}
	model := opts.Model
	effort := opts.Effort
	// normalizedName is the optional user-supplied title (nil/empty when the
	// user gave none). storedName is the resolved NON-NULL value persisted on
	// the row/Info — assigned the canonical SessionDisplayName default below
	// once the session id (and thus pod name) is known. The pod annotation is
	// stamped with storedName too so degraded pod-only reads match the row.
	normalizedName := sessionmodel.NormalizeName(opts.Name)

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
	if m.antigravityAPIProxyIP == "" {
		m.antigravityAPIProxyIP = resolveIP(os.Getenv("ANTIGRAVITY_API_PROXY_HOST"))
	}

	sessionID, err := m.nextSessionID(ctx)
	if err != nil {
		return Info{}, err
	}

	// Resolve the stored name to NON-NULL: the user's title when supplied,
	// else the canonical SessionDisplayName default (short id derived from
	// the pod name). storedName is what lands on the durable row, the pod
	// annotation, and the Info.
	podName := "session-" + sessionID
	storedName := sessionmodel.SessionDisplayName(normalizedName, podName, sessionID)

	contextJSON := ""
	if glimmungContext != nil {
		b, _ := json.Marshal(glimmungContext)
		contextJSON = string(b)
	}

	manifestOpts := m.manifestOpts
	manifestOpts.OAuthGatewayIP = m.oauthGatewayIP
	manifestOpts.APIProxyIP = m.apiProxyIP
	manifestOpts.CodexAPIProxyIP = m.codexAPIProxyIP
	manifestOpts.AntigravityAPIProxyIP = m.antigravityAPIProxyIP
	manifestOpts.GlimmungContextJSON = contextJSON
	manifestOpts.Repos = repos
	manifestOpts.Name = &storedName
	manifestOpts.Capabilities = capabilities
	manifestOpts.Model = model
	manifestOpts.Effort = effort
	m.applyImageOverride(ctx, &manifestOpts, mode)
	sessionImage := sessionmodel.ResolvedSessionImage(mode, manifestOpts)
	sessionImageMetadata := sessionmodel.ResolvedSessionImageMetadata(mode, manifestOpts)

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
	// the row visible=false so the snapshot stops returning it. podName is
	// resolved above (alongside storedName).
	assignment, reserved, err := m.reserveSessionAvatars(ctx, owner, sessionID)
	if err != nil {
		return Info{}, err
	}
	if reserved && assignment.AgentAvatarID == "" {
		return Info{}, fmt.Errorf("reserve session avatars: no agent avatars available")
	}
	if m.registry != nil {
		if regErr := m.registry.Upsert(ctx, sessionmodel.SessionRecord{
			ID:                   sessionID,
			Email:                owner,
			Mode:                 mode,
			Scope:                m.manifestOpts.SessionScope,
			PodName:              podName,
			SessionImage:         sessionImage,
			SessionImageMetadata: sessionImageMetadata,
			Visible:              true,
			Name:                 storedName,
			RequestedAt:          requestedAt,
			UpdatedAt:            requestedAt,
			Repos:                repos,
			Capabilities:         capabilities,
			Model:                model,
			Effort:               effort,
			AgentAvatarID:        assignment.AgentAvatarID,
			SystemAvatarID:       assignment.SystemAvatarID,
		}); regErr != nil {
			slog.Warn("create registry upsert failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
		if len(opts.BugLabels) > 0 {
			if regErr := m.registry.SetBugLabels(ctx, owner, sessionID, opts.BugLabels); regErr != nil {
				return Info{}, regErr
			}
		} else if opts.BugLabel != nil {
			if regErr := m.registry.SetBugLabel(ctx, owner, sessionID, opts.BugLabel); regErr != nil {
				return Info{}, regErr
			}
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

	var createdAt *string
	if !created.CreationTimestamp.IsZero() {
		s := created.CreationTimestamp.UTC().Format("2006-01-02T15:04:05+00:00")
		createdAt = &s
	}

	bugLabels := opts.BugLabels
	if len(bugLabels) == 0 && opts.BugLabel != nil {
		bugLabels = []*sessionmodel.SessionBugLabel{opts.BugLabel}
	}
	bugLabel := opts.BugLabel
	if bugLabel == nil && len(bugLabels) > 0 {
		bugLabel = bugLabels[0]
	}
	info := Info{
		ID:                   sessionID,
		PodName:              &podName,
		Owner:                owner,
		Status:               "Pending",
		Mode:                 mode,
		SessionImage:         sessionImage,
		SessionImageMetadata: sessionImageMetadata,
		RequestedAt:          &requestedAt,
		CreatedAt:            createdAt,
		Name:                 storedName,
		Repos:                repos,
		Capabilities:         capabilities,
		BugLabel:             bugLabel,
		BugLabels:            bugLabels,
		Model:                model,
		Effort:               effort,
		AgentAvatarID:        assignment.AgentAvatarID,
		SystemAvatarID:       assignment.SystemAvatarID,
	}

	// Refresh the registry row with the K8s-assigned created_at so the
	// snapshot's CreatedAt matches the pod object's creation timestamp.
	if m.registry != nil && createdAt != nil {
		if regErr := m.registry.Upsert(ctx, sessionmodel.SessionRecord{
			ID:                   sessionID,
			Email:                owner,
			Mode:                 mode,
			Scope:                m.manifestOpts.SessionScope,
			PodName:              podName,
			SessionImage:         sessionImage,
			SessionImageMetadata: sessionImageMetadata,
			Visible:              true,
			Name:                 storedName,
			RequestedAt:          requestedAt,
			CreatedAt:            *createdAt,
			UpdatedAt:            requestedAt,
			Repos:                repos,
			Capabilities:         capabilities,
			Model:                model,
			Effort:               effort,
			AgentAvatarID:        assignment.AgentAvatarID,
			SystemAvatarID:       assignment.SystemAvatarID,
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
func (m *Manager) Delete(ctx context.Context, owner, sessionID string) error {
	// Pod lookup is best-effort; ErrNotFound still allows the registry mark below.
	pod, err := m.findPodBySessionID(ctx, owner, sessionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if pod != nil {
		if delErr := m.client.CoreV1().Pods(m.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
			return fmt.Errorf("delete pod: %w", delErr)
		}
	}

	if m.registry != nil {
		if regErr := m.registry.MarkDeleted(ctx, owner, sessionID); regErr != nil {
			slog.Warn("delete registry mark-deleted failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}
	m.publishRow(ctx, owner, sessionID)
	return nil
}

// SetName updates the session name on the pod annotation and registry. Name
// is NON-NULL: clearing (nil/empty input) no longer stores null — it reassigns
// the canonical SessionDisplayName default (the short id derived from the pod
// name), the same value Create assigns to an unnamed session.
func (m *Manager) SetName(ctx context.Context, owner, sessionID string, name *string) (Info, error) {
	normalized := sessionmodel.NormalizeName(name)
	podName := "session-" + sessionID
	resolvedName := sessionmodel.SessionDisplayName(normalized, podName, sessionID)

	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				"tank-operator/display-name": resolvedName,
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
		// Persist the resolved NON-NULL name; clearing writes the default, not null.
		if regErr := m.registry.SetName(ctx, owner, sessionID, &resolvedName); regErr != nil {
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

// SetOpenTarget persists the legacy durable per-session sidebar open-target
// preference (empty string / 'chat' / 'turns'). Like SetBugLabel it is registry-only UI
// state, so no pod annotation is patched. Validation lives in the HTTP handler;
// the manager just persists the value, publishes the updated row, and returns the refreshed
// Info the same way SetName's tail does.
func (m *Manager) SetOpenTarget(ctx context.Context, owner, sessionID, target string) (Info, error) {
	if m.registry != nil {
		if regErr := m.registry.SetOpenTarget(ctx, owner, sessionID, target); regErr != nil {
			slog.Warn("set-open-target registry update failed",
				"session_id", sessionID, "owner", owner, "error", regErr)
		}
	}
	m.publishRow(ctx, owner, sessionID)

	if registered, err := m.GetRegisteredByOwner(ctx, owner, sessionID); err == nil {
		return registered, nil
	}
	return m.GetByOwner(ctx, owner, sessionID)
}

const maxBugLabelsPerSession = 5

// SetBugLabel attaches or clears the optional Tank-native bug label for a
// session. It is registry-only state, so no pod annotation is patched.
func (m *Manager) SetBugLabel(ctx context.Context, owner, sessionID string, labelName *string) (Info, error) {
	if labelName == nil {
		return m.SetBugLabels(ctx, owner, sessionID, nil)
	}
	return m.SetBugLabels(ctx, owner, sessionID, []string{*labelName})
}

// SetBugLabels replaces the Tank-native bug labels for a session.
func (m *Manager) SetBugLabels(ctx context.Context, owner, sessionID string, labelNames []string) (Info, error) {
	if len(labelNames) > maxBugLabelsPerSession {
		return Info{}, fmt.Errorf("too many bug labels: %d > %d", len(labelNames), maxBugLabelsPerSession)
	}
	labels := make([]*sessionmodel.SessionBugLabel, 0, len(labelNames))
	seen := map[string]struct{}{}
	for i := range labelNames {
		label, err := sessionmodel.NormalizeBugLabelName(&labelNames[i])
		if err != nil {
			return Info{}, err
		}
		if label == nil {
			continue
		}
		if _, ok := seen[label.Slug]; ok {
			continue
		}
		seen[label.Slug] = struct{}{}
		labels = append(labels, label)
	}
	if len(labels) > maxBugLabelsPerSession {
		return Info{}, fmt.Errorf("too many bug labels: %d > %d", len(labels), maxBugLabelsPerSession)
	}
	if _, err := m.GetRegisteredByOwner(ctx, owner, sessionID); err != nil {
		return Info{}, err
	}
	if m.registry != nil {
		if regErr := m.registry.SetBugLabels(ctx, owner, sessionID, labels); regErr != nil {
			return Info{}, regErr
		}
	}
	m.publishRow(ctx, owner, sessionID)
	return m.GetRegisteredByOwner(ctx, owner, sessionID)
}

// SetTestState updates the row's test_state column AND patches the
// matching pod annotation (the session-agent reads the annotation via
// the projected downward-API volume). Both writes are load-bearing
// in steady state: the column is the snapshot-facing replica; the
// annotation is what the agent code path consults.
func (m *Manager) SetTestState(ctx context.Context, owner, sessionID string, active bool, slotIndex *int, url *string, pullRequestURL *string) (Info, error) {
	state := map[string]any{"active": active}
	if slotIndex != nil {
		state["slot_index"] = *slotIndex
	}
	if url != nil && *url != "" {
		state["url"] = *url
	}
	if pullRequestURL != nil && strings.TrimSpace(*pullRequestURL) != "" {
		state["pull_request_url"] = strings.TrimSpace(*pullRequestURL)
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

// SetTestPullRequestURL updates only the PR link carried by the test workflow
// state. It preserves any existing test slot URL/index so an agent can post
// the draft PR after opening it without reconstructing the whole test pill.
func (m *Manager) SetTestPullRequestURL(ctx context.Context, owner, sessionID string, url *string) (Info, error) {
	state := map[string]any{"active": true}
	if registered, err := m.GetRegisteredByOwner(ctx, owner, sessionID); err == nil && registered.TestState != nil {
		for key, value := range registered.TestState {
			state[key] = value
		}
	} else if current, err := m.GetByOwner(ctx, owner, sessionID); err == nil && current.TestState != nil {
		for key, value := range current.TestState {
			state[key] = value
		}
	}
	normalized := ""
	if url != nil {
		normalized = strings.TrimSpace(*url)
	}
	if normalized == "" {
		delete(state, "pull_request_url")
	} else {
		state["active"] = true
		state["pull_request_url"] = normalized
	}
	raw, _ := json.Marshal(state)
	annotations := map[string]string{testStateAnnotation: string(raw)}
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

type runtimeContextWindowRegistry interface {
	SetRuntimeContextWindow(ctx context.Context, email, sessionID string, tokens int64, source string) error
}

type providerRateLimitRegistry interface {
	SetProviderRateLimitInfo(ctx context.Context, email, sessionID string, info map[string]any) error
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

// SetRuntimeContextWindow records the first provider-observed model context
// window for the immutable session runtime. Repeated reports of the same
// provider fact are no-ops in the store; callers still receive the current row.
func (m *Manager) SetRuntimeContextWindow(ctx context.Context, owner, sessionID string, tokens int64, source string) (Info, error) {
	registry, ok := m.registry.(runtimeContextWindowRegistry)
	if !ok {
		return Info{}, ErrNotFound
	}
	if err := registry.SetRuntimeContextWindow(ctx, owner, sessionID, tokens, source); err != nil {
		return Info{}, err
	}
	m.publishRow(ctx, owner, sessionID)
	return m.GetRegisteredByOwner(ctx, owner, sessionID)
}

// SetProviderRateLimitInfo records provider-specific rate-limit metadata
// reported by the session runner and publishes the updated session row.
func (m *Manager) SetProviderRateLimitInfo(ctx context.Context, owner, sessionID string, info map[string]any) (Info, error) {
	registry, ok := m.registry.(providerRateLimitRegistry)
	if !ok {
		return Info{}, ErrNotFound
	}
	if err := registry.SetProviderRateLimitInfo(ctx, owner, sessionID, info); err != nil {
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
// that should only expose sidebar-visible sessions and do not require a live
// pod. Transcript surfaces use GetRegisteredByOwnerAnyVisibility instead:
// copied transcript links are explicit durable-history reads, while
// visible=false is only the sidebar tombstone.
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

// GetRegisteredByOwnerAnyVisibility retrieves a durable session row for
// transcript history reads. The returned bool reports the row's sidebar
// visibility so callers can emit observability for explicit reads of
// soft-deleted sessions without leaking the flag on the public Info wire.
//
// The registry row is the cold-open authority for copied transcript links and
// MCP transcript reads: pod death and sidebar tombstoning must not hide durable
// conversation history from an authorized owner/admin.
func (m *Manager) GetRegisteredByOwnerAnyVisibility(ctx context.Context, owner, sessionID string) (Info, bool, error) {
	getter, ok := m.registry.(sessionRegistryGetter)
	if !ok {
		return Info{}, false, ErrNotFound
	}
	record, found, err := getter.Get(ctx, owner, sessionID)
	if err != nil {
		return Info{}, false, err
	}
	if !found {
		return Info{}, false, ErrNotFound
	}
	return infoFromRecord(owner, record), record.Visible, nil
}

// RegisteredOwnerForSession resolves a session id to its durable registry
// owner without consulting Kubernetes. It intentionally includes invisible
// rows so admin transcript reads can recover soft-deleted conversation history
// without flipping sidebar state back to visible.
func (m *Manager) RegisteredOwnerForSession(ctx context.Context, scope, sessionID string) (string, error) {
	resolver, ok := m.registry.(sessionRegistryOwnerResolver)
	if !ok {
		return "", nil
	}
	if scope == "" {
		scope = m.scope
	}
	return resolver.OwnerForSession(ctx, scope, sessionID)
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
