// nats-auth-callout is the per-session NATS credential issuer
// (tank-operator#1128). Session pods stop sharing one fleet-wide NATS token;
// each pod authenticates with its projected ServiceAccount token (audience
// auth.romaine.life — the same trust root as the MCP gateway) and receives a
// NATS user JWT whose permissions cover exactly its own session's subjects:
//
//   - publish tank.session.<scope>.<sid>.events       (event ledger ingress)
//   - the TANK_SESSION_COMMANDS JetStream consumer API for the session's
//     own per-provider durables (data + control planes)
//   - subscribe _INBOX.> (JS API replies and pull deliveries)
//
// Identity is NOT taken from the client's claimed username: the SA token is
// validated via TokenReview (audience-pinned), the pod name comes from the
// token's bound-pod claim, and the session binding is read from the pod's
// orchestrator-written labels (tank-operator/session-id, -scope). A pod can
// only ever get permissions for the session the orchestrator bound it to.
//
// Transition (#1128 staging): legacy pods that still present the shared
// fleet token are granted unrestricted permissions here, preserving the
// pre-callout behavior until they age out; the orchestrator itself is a
// static auth_users entry in the NATS server config and never reaches this
// service — a callout outage therefore cannot take down the command plane,
// only delay NEW session pods' connections (JetStream redelivers).
package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

// sessionResolver is the Kubernetes seam: token → pod, pod → session.
type sessionResolver interface {
	// ResolvePodFromToken validates a projected SA token (audience-pinned
	// TokenReview) and returns the bound pod's name. An error means the
	// token is invalid, expired, lacks the audience, or is not bound to a
	// pod of the session ServiceAccount.
	ResolvePodFromToken(ctx context.Context, token string) (string, error)
	// SessionStorageKeyForPod returns the storage key recorded on the pod's
	// orchestrator-written labels (tank-operator/session-id + -scope).
	SessionStorageKeyForPod(ctx context.Context, podName string) (string, error)
}

type calloutService struct {
	issuer nkeys.KeyPair
	// account the issued users land in. Server-config (operator-less) mode
	// runs everything in the global account.
	account  string
	resolver sessionResolver
	// legacyToken, when non-empty, grants pre-#1128 unrestricted
	// permissions to clients presenting the shared fleet token — the
	// migration policy's pre-deploy-pod clause: existing pods hold that
	// token until they age out.
	legacyToken string
	// commandStream is the WorkQueue stream carrying session commands.
	commandStream string
	// providers whose per-session durables a session pod may own. The
	// provider is deliberately not read from pod labels: granting a
	// session's OWN other-provider durables is harmless (same session,
	// same isolation boundary) and keeps the callout decoupled from mode
	// taxonomy churn.
	providers []string
	userTTL   time.Duration
	now       func() time.Time
}

const (
	defaultCommandStream = "TANK_SESSION_COMMANDS"
	defaultUserTTL       = 12 * time.Hour
	// natsGlobalAccount is where conf-mode (operator-less) users live.
	natsGlobalAccount = "$G"
)

var defaultProviders = []string{"claude", "claude_secondary", "codex", "antigravity"}

// Handle processes one $SYS.REQ.USER.AUTH request and returns the encoded
// authorization response JWT. Every return is a valid response document —
// rejections travel in the response's Error field so the server can log a
// precise reason instead of timing the client out.
func (s *calloutService) Handle(ctx context.Context, requestJWT []byte) ([]byte, error) {
	req, err := jwt.DecodeAuthorizationRequestClaims(string(requestJWT))
	if err != nil {
		// Without a decoded request there is no user nkey to address a
		// response to; the server treats the lack of a response as deny.
		return nil, fmt.Errorf("decode authorization request: %w", err)
	}
	respond := func(userJWT, errMsg string) ([]byte, error) {
		rc := jwt.NewAuthorizationResponseClaims(req.UserNkey)
		rc.Audience = req.Server.ID
		rc.Jwt = userJWT
		rc.Error = errMsg
		encoded, err := rc.Encode(s.issuer)
		if err != nil {
			return nil, fmt.Errorf("encode authorization response: %w", err)
		}
		return []byte(encoded), nil
	}

	// Transition arm: the shared fleet token, presented either via the
	// dedicated token field (nats.Token / nats.js token:) or as a bare
	// password, keeps unrestricted permissions until pre-#1128 pods age out.
	presented := strings.TrimSpace(req.ConnectOptions.Token)
	if presented == "" && strings.TrimSpace(req.ConnectOptions.Username) == "" {
		presented = strings.TrimSpace(req.ConnectOptions.Password)
	}
	if s.legacyToken != "" && presented != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(s.legacyToken)) == 1 {
		userJWT, err := s.legacyUserJWT(req)
		if err != nil {
			return respond("", "legacy user issuance failed")
		}
		recordCalloutAuth("legacy")
		return respond(userJWT, "")
	}

	saToken := strings.TrimSpace(req.ConnectOptions.Password)
	if saToken == "" {
		recordCalloutAuth("denied_no_credentials")
		return respond("", "no credentials: expected projected SA token as password")
	}
	podName, err := s.resolver.ResolvePodFromToken(ctx, saToken)
	if err != nil {
		recordCalloutAuth("denied_token_invalid")
		slog.Warn("nats auth callout rejected SA token", "error", err)
		return respond("", "service account token rejected")
	}
	storageKey, err := s.resolver.SessionStorageKeyForPod(ctx, podName)
	if err != nil {
		recordCalloutAuth("denied_pod_unbound")
		slog.Warn("nats auth callout could not bind pod to session", "pod", podName, "error", err)
		return respond("", "pod has no session binding")
	}
	// The claimed username is advisory; if present it must agree with the
	// orchestrator-written binding (catches mis-wired pods loudly instead
	// of silently granting a different session's permissions).
	if claimed := strings.TrimSpace(req.ConnectOptions.Username); claimed != "" && claimed != storageKey {
		recordCalloutAuth("denied_identity_mismatch")
		slog.Warn("nats auth callout identity mismatch", "pod", podName, "claimed", claimed, "bound", storageKey)
		return respond("", "claimed identity does not match pod's session binding")
	}
	userJWT, err := s.sessionUserJWT(req, storageKey)
	if err != nil {
		recordCalloutAuth("error")
		slog.Error("nats auth callout user issuance failed", "pod", podName, "error", err)
		return respond("", "user issuance failed")
	}
	recordCalloutAuth("session")
	slog.Info("nats auth callout issued session user", "pod", podName, "storage_key", storageKey)
	return respond(userJWT, "")
}

// sessionUserJWT issues the per-session permission set. The subject and
// durable shapes mirror internal/sessionbus and runner-shared/sessionBus.js
// EXACTLY — sessionbus is the single source of truth on the Go side.
func (s *calloutService) sessionUserJWT(req *jwt.AuthorizationRequestClaims, storageKey string) (string, error) {
	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Name = "session-" + storageKey
	uc.Audience = s.account
	uc.Expires = s.now().Add(s.userTTL).Unix()

	pub := []string{
		sessionbus.SessionEventSubject(storageKey),
		"$JS.API.INFO",
	}
	for _, durable := range sessionDurableNames(storageKey, s.providers) {
		pub = append(pub,
			"$JS.API.CONSUMER.DURABLE.CREATE."+s.commandStream+"."+durable,
			"$JS.API.CONSUMER.CREATE."+s.commandStream+"."+durable,
			"$JS.API.CONSUMER.CREATE."+s.commandStream+"."+durable+".>",
			"$JS.API.CONSUMER.INFO."+s.commandStream+"."+durable,
			"$JS.API.CONSUMER.MSG.NEXT."+s.commandStream+"."+durable,
		)
	}
	uc.Permissions.Pub.Allow.Add(pub...)
	// JS API replies and pull deliveries arrive on the client's inbox.
	uc.Permissions.Sub.Allow.Add("_INBOX.>")
	return uc.Encode(s.issuer)
}

// legacyUserJWT preserves pre-callout behavior for the shared fleet token.
func (s *calloutService) legacyUserJWT(req *jwt.AuthorizationRequestClaims) (string, error) {
	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Name = "legacy-shared-token"
	uc.Audience = s.account
	uc.Expires = s.now().Add(s.userTTL).Unix()
	uc.Permissions.Pub.Allow.Add(">")
	uc.Permissions.Sub.Allow.Add(">")
	return uc.Encode(s.issuer)
}

// sessionDurableNames mirrors runner-shared/sessionBus.js consumerName() /
// controlConsumerName(): <provider>_<scopeToken>_<sessionIDToken> and
// <provider>_control_<scopeToken>_<sessionIDToken>. StorageToken's base64url
// alphabet (A-Za-z0-9_-) is dot-free, so each durable is one NATS token and
// the $JS.API permission subjects above cannot be widened by crafted input.
func sessionDurableNames(storageKey string, providers []string) []string {
	scope, sessionID := sessionbus.StorageScopeAndSessionID(storageKey)
	suffix := sessionbus.ScopeToken(scope) + "_" + sessionbus.SessionIDToken(sessionID)
	out := make([]string, 0, len(providers)*2)
	for _, provider := range providers {
		p := sanitizeConsumerProvider(provider)
		out = append(out, p+"_"+suffix, p+"_control_"+suffix)
	}
	return out
}

// sanitizeConsumerProvider mirrors runner-shared sanitizeConsumerName:
// sanitizeSubjectToken (lowercase; anything outside [a-z0-9_-] becomes _)
// followed by dash→underscore, because '-' is reserved as the separator in
// orchestrator-owned consumer names (see sweep.go DecodeConsumerSessionID).
func sanitizeConsumerProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
