package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// keyResolveTimeout caps how long a verify call can spend fetching a missing
// public key from KV. Verify is on the request path; a stalled KV call must
// not block forever.
const keyResolveTimeout = 5 * time.Second

type User struct {
	Sub   string
	Email string
	Name  string
	// Role is the platform-wide claim carried in the auth.romaine.life JWT.
	// "admin" gets bypasses (e.g. OnboardingWall); "user" is the standard
	// signed-in caller; "service" is a k8s service-principal minted by
	// auth.romaine.life via /api/auth/exchange/k8s for session pods that
	// need to call tank-operator on their own behalf (see nelsong6/tank-operator#486).
	// Any other value (including the empty string) is rejected by Decode -
	// that's how a "pending" auth.romaine.life user who hasn't been
	// promoted by an admin gets kept out of tank-operator.
	Role string
	// ActorEmail carries the human owner whose pod is making the call.
	// Only populated when Role == RoleService - service principals carry
	// this claim from the upstream exchange so handlers can scope writes
	// to the actor's session tree (a child session opened by pod X must
	// be owned by the same human who owns pod X's parent session).
	// Empty for human roles; the human's own email is in Email.
	ActorEmail string
	// Purpose mirrors the optional auth.romaine.life `purpose` claim. The
	// only value tank-operator acts on is "bot": auth.romaine.life's
	// "Mint bot token" break-glass flow stamps purpose=bot on a 24h
	// admin JWT that carries the admin's own email (no actor_email - it
	// is not a service principal). Tank uses this purely to attribute
	// turns submitted by automation rather than an interactive human, so
	// the transcript renders the session's system identity instead of the
	// human owner's Gravatar. It grants no authority of its own; the role
	// claim is still the access gate. Empty for ordinary browser sign-ins.
	Purpose string
}

const (
	RoleAdmin   = "admin"
	RoleUser    = "user"
	RoleService = "service"
)

// PurposeBot is the auth.romaine.life `purpose` claim value carried by
// admin "bot" tokens minted through the break-glass console. See User.Purpose.
const PurposeBot = "bot"

// allowedRoles is the closed set of roles this service accepts from
// auth.romaine.life. auth.romaine.life mints `pending` by default for any
// fresh Microsoft sign-in; an admin promotes via auth.romaine.life's /admin
// console before the user becomes useful here. `service` is accepted but
// per-route gating is required: no human-facing route should accept service
// callers by default, and no service-only route should accept human callers.
var allowedRoles = map[string]struct{}{
	RoleAdmin:   {},
	RoleUser:    {},
	RoleService: {},
}

// IsService reports whether the user is a k8s service principal.
func (u User) IsService() bool { return u.Role == RoleService }

// IsBot reports whether the caller authenticated with an auth.romaine.life
// bot token (purpose=bot). Used only for transcript authorship attribution -
// a bot-authored turn is rendered as the session's system identity rather
// than the human owner. Not an authorization signal.
func (u User) IsBot() bool { return u.Purpose == PurposeBot }

// IsHuman reports whether the user is a human (admin or user, not a
// service principal). Use this to gate routes that must reject service
// callers without enumerating both human roles at each call site.
func (u User) IsHuman() bool { return u.Role == RoleAdmin || u.Role == RoleUser }

// OwnerEmail returns the human owner email that should scope session-owned
// resources. Human callers own resources as themselves; service principals
// act on behalf of the actor_email stamped by auth.romaine.life.
func (u User) OwnerEmail() string {
	if u.Role == RoleService && u.ActorEmail != "" {
		return u.ActorEmail
	}
	return u.Email
}

type Verifier struct {
	resolver KeyResolver
}

func NewVerifier(resolver KeyResolver) *Verifier {
	return &Verifier{resolver: resolver}
}

func (v *Verifier) CurrentUser(r *http.Request) (User, error) {
	token, err := tokenFromRequest(r)
	if err != nil {
		return User{}, err
	}
	return v.Decode(token)
}

func (v *Verifier) Decode(tokenString string) (User, error) {
	if v.resolver == nil {
		return User{}, errHTTP{status: http.StatusInternalServerError, message: "JWT key resolver not configured"}
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token missing kid")
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyResolveTimeout)
		defer cancel()
		return v.resolver.PublicKey(ctx, kid)
	}, jwt.WithLeeway(60*time.Second), jwt.WithIssuer(authRomaineLifeIssuer))
	if err != nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid token")
		}
		return User{}, errHTTP{status: http.StatusUnauthorized, message: "invalid session token: " + err.Error()}
	}

	email := strings.ToLower(stringClaim(claims, "email"))
	if email == "" {
		return User{}, errHTTP{status: http.StatusUnauthorized, message: "invalid session token: missing email"}
	}
	role := stringClaim(claims, "role")
	if _, ok := allowedRoles[role]; !ok {
		return User{}, errHTTP{status: http.StatusForbidden, message: "role not accepted: " + role}
	}
	user := User{
		Sub:     stringClaim(claims, "sub"),
		Email:   email,
		Name:    stringClaim(claims, "name"),
		Role:    role,
		Purpose: strings.ToLower(strings.TrimSpace(stringClaim(claims, "purpose"))),
	}
	if role == RoleService {
		// Service principals MUST carry an actor_email claim - without it
		// there is no way to scope side-effects to a specific human. The
		// upstream exchange (auth.romaine.life /api/auth/exchange/k8s)
		// refuses to mint such a token, so seeing one here means either
		// upstream regression or a forged/stale token. Reject loud.
		actor := strings.ToLower(stringClaim(claims, "actor_email"))
		if actor == "" {
			return User{}, errHTTP{
				status:  http.StatusUnauthorized,
				message: "service-role token missing actor_email claim",
			}
		}
		user.ActorEmail = actor
	}
	return user, nil
}

func tokenFromRequest(r *http.Request) (string, error) {
	if authorization := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return strings.TrimSpace(authorization[7:]), nil
	}
	return "", errHTTP{status: http.StatusUnauthorized, message: "missing authentication"}
}

func stringClaim(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}

func GravatarURL(email string, size int) string {
	if size <= 0 {
		size = 64
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	sum := md5.Sum([]byte(normalized))
	return fmt.Sprintf("https://www.gravatar.com/avatar/%s?s=%d&d=mp", hex.EncodeToString(sum[:]), size)
}

type errHTTP struct {
	status  int
	message string
}

func (e errHTTP) Error() string {
	return e.message
}

func ErrorStatus(err error) int {
	var httpErr errHTTP
	if errors.As(err, &httpErr) {
		return httpErr.status
	}
	return http.StatusInternalServerError
}
