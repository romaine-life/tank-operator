// nats-auth-callout is the per-session NATS credential issuer
// (tank-operator#1128). Session pods stop sharing one fleet-wide NATS token;
// each pod authenticates with the same auth.romaine.life pod identity path
// used by the rest of the platform and receives a
// NATS user JWT whose permissions cover exactly its own session's subjects:
//
//   - publish tank.session.<scope>.<sid>.events       (event ledger ingress)
//   - the TANK_SESSION_COMMANDS JetStream consumer API for the session's
//     own per-provider durables (data + control planes)
//   - subscribe _INBOX.> (JS API replies and pull deliveries)
//
// Identity is NOT taken from the client's claimed username: the presented pod
// token is exchanged at auth.romaine.life, the returned service JWT is verified
// against the platform JWKS, and the NATS storage key is derived from that
// service identity. A pod can only ever get permissions for the session that
// auth.romaine.life's pod-lineage exchange bound it to.
//
// The orchestrator itself is a static auth_users entry in the NATS server
// config and never reaches this service — a callout outage therefore cannot
// take down the command plane, only delay NEW session pods' connections
// (JetStream redelivers).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

// sessionResolver is the identity seam: presented credential -> storage key.
type sessionResolver interface {
	// SessionStorageKeyFromToken validates the presented pod credential and
	// returns the NATS storage key it is allowed to claim.
	SessionStorageKeyFromToken(ctx context.Context, token string) (string, error)
}

type calloutService struct {
	issuer nkeys.KeyPair
	// account the issued users land in. Server-config (operator-less) mode
	// runs everything in the global account.
	account  string
	resolver sessionResolver
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

var defaultProviders = []string{"claude", "claude_secondary", "codex"}

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

	podToken := strings.TrimSpace(req.ConnectOptions.Password)
	if podToken == "" {
		recordCalloutAuth("denied_no_credentials")
		return respond("", "no credentials: expected pod identity token as password")
	}
	storageKey, err := s.resolver.SessionStorageKeyFromToken(ctx, podToken)
	if err != nil {
		recordCalloutAuth(calloutDenyResult(err, "denied_token_invalid"))
		slog.Warn("nats auth callout rejected pod identity", "error", err)
		return respond("", "pod identity rejected")
	}
	// The claimed username is advisory; if present it must agree with the
	// auth.romaine.life binding (catches mis-wired pods loudly instead
	// of silently granting a different session's permissions).
	if claimed := strings.TrimSpace(req.ConnectOptions.Username); claimed != "" && claimed != storageKey {
		recordCalloutAuth("denied_identity_mismatch")
		slog.Warn("nats auth callout identity mismatch", "claimed", claimed, "bound", storageKey)
		return respond("", "claimed identity does not match pod identity")
	}
	userJWT, err := s.sessionUserJWT(req, storageKey)
	if err != nil {
		recordCalloutAuth("error")
		slog.Error("nats auth callout user issuance failed", "storage_key", storageKey, "error", err)
		return respond("", "user issuance failed")
	}
	recordCalloutAuth("session")
	slog.Info("nats auth callout issued session user", "storage_key", storageKey)
	return respond(userJWT, "")
}

type calloutDenyError struct {
	result string
	err    error
}

func (e calloutDenyError) Error() string {
	return e.err.Error()
}

func (e calloutDenyError) Unwrap() error {
	return e.err
}

func deny(result string, err error) error {
	return calloutDenyError{result: result, err: err}
}

func calloutDenyResult(err error, fallback string) string {
	var denied calloutDenyError
	if errors.As(err, &denied) && strings.TrimSpace(denied.result) != "" {
		return denied.result
	}
	return fallback
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
			"$JS.ACK."+s.commandStream+"."+durable+".>",
		)
	}
	uc.Permissions.Pub.Allow.Add(pub...)
	// JS API replies and pull deliveries arrive on the client's inbox.
	uc.Permissions.Sub.Allow.Add("_INBOX.>")
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
