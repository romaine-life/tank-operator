package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// natsAuthCalloutSubject is where the NATS server sends authorization
// requests when authorization.auth_callout is configured.
const natsAuthCalloutSubject = "$SYS.REQ.USER.AUTH"

// defaultAuthExchangeURL is the platform service-account token exchange used
// by session pods. NATS auth intentionally reuses the same identity provider
// path instead of reading Kubernetes pod metadata itself.
const defaultAuthExchangeURL = "https://auth.romaine.life/api/auth/exchange/k8s"

const (
	defaultSessionScope = "default"
)

var (
	tankServiceStableIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	tankServiceSlotStableIDPattern = regexp.MustCompile(`^slot-([1-9][0-9]*)-session-(.+)$`)
)

var calloutAuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "tank_nats_auth_callout_total",
	Help: "NATS auth-callout outcomes: session (per-session JWT issued), denied_*, error.",
}, []string{"result"})

func recordCalloutAuth(result string) {
	calloutAuthTotal.WithLabelValues(result).Inc()
}

// authExchangeSessionResolver delegates pod-token validation and pod-lineage
// lookup to auth.romaine.life, then verifies the returned platform JWT locally.
type authExchangeSessionResolver struct {
	http        *http.Client
	exchangeURL string
	verifier    *auth.Verifier
}

func (r *authExchangeSessionResolver) SessionStorageKeyFromToken(ctx context.Context, token string) (string, error) {
	authToken, err := r.exchange(ctx, token)
	if err != nil {
		return "", err
	}
	user, err := r.verifier.Decode(authToken)
	if err != nil {
		return "", deny("denied_auth_jwt_invalid", err)
	}
	return storageKeyFromAuthUser(user)
}

func (r *authExchangeSessionResolver) exchange(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.exchangeURL, bytes.NewBufferString("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", deny("denied_auth_exchange", fmt.Errorf("auth exchange returned %d: %s", resp.StatusCode, responseDetail(resp)))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode auth exchange response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return "", errors.New("auth exchange response missing token")
	}
	return strings.TrimSpace(payload.Token), nil
}

func storageKeyFromAuthUser(user auth.User) (string, error) {
	if !user.IsService() {
		return "", deny("denied_subject_untrusted", fmt.Errorf("auth token role %q is not service", user.Role))
	}
	const subPrefix = "svc:tank:"
	stableID, ok := strings.CutPrefix(strings.TrimSpace(user.Sub), subPrefix)
	if !ok || stableID == "" {
		return "", deny("denied_subject_untrusted", fmt.Errorf("auth token subject %q is not a tank session service principal", user.Sub))
	}
	if !tankServiceStableIDPattern.MatchString(stableID) {
		return "", deny("denied_subject_untrusted", fmt.Errorf("auth token subject %q carries invalid stable id", user.Sub))
	}
	wantEmail := "pod-" + stableID + "@service.tank.romaine.life"
	if user.Email != wantEmail {
		return "", deny("denied_subject_untrusted", fmt.Errorf("auth token email %q does not match subject %q", user.Email, user.Sub))
	}
	if match := tankServiceSlotStableIDPattern.FindStringSubmatch(stableID); match != nil {
		sessionID := strings.TrimSpace(match[2])
		if sessionID == "" {
			return "", deny("denied_subject_untrusted", fmt.Errorf("empty slot session id in subject %q", user.Sub))
		}
		return "tank-operator-slot-" + match[1] + ":" + sessionID, nil
	}
	return stableID, nil
}

func responseDetail(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return http.StatusText(resp.StatusCode)
	}
	return detail
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func requiredEnv(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		slog.Error("required environment variable missing", "name", name)
		os.Exit(1)
	}
	return v
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	issuerSeed := requiredEnv("NATS_CALLOUT_ISSUER_SEED")
	issuer, err := nkeys.FromSeed([]byte(issuerSeed))
	if err != nil {
		slog.Error("issuer seed invalid", "error", err)
		os.Exit(1)
	}
	if _, err := issuer.PublicKey(); err != nil {
		slog.Error("issuer seed unusable", "error", err)
		os.Exit(1)
	}

	svc := &calloutService{
		issuer:  issuer,
		account: natsGlobalAccount,
		resolver: &authExchangeSessionResolver{
			http:        &http.Client{Timeout: 5 * time.Second},
			exchangeURL: env("NATS_CALLOUT_AUTH_EXCHANGE_URL", defaultAuthExchangeURL),
			verifier:    auth.NewVerifier(auth.NewRomaineLifeKeyResolver()),
		},
		commandStream: env("NATS_CALLOUT_COMMAND_STREAM", defaultCommandStream),
		providers:     defaultProviders,
		userTTL:       defaultUserTTL,
		now:           time.Now,
	}

	nc, err := nats.Connect(
		requiredEnv("NATS_URL"),
		nats.UserInfo(requiredEnv("NATS_CALLOUT_USER"), requiredEnv("NATS_CALLOUT_PASSWORD")),
		nats.Name("tank-nats-auth-callout"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	defer nc.Drain() //nolint:errcheck

	// Queue subscription: every callout replica may serve any request.
	sub, err := nc.QueueSubscribe(natsAuthCalloutSubject, "tank-nats-auth-callout", func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := svc.Handle(ctx, msg.Data)
		if err != nil {
			// No decodable request → no addressable response; the server
			// treats silence as deny.
			slog.Warn("auth callout request unhandled", "error", err)
			return
		}
		if err := msg.Respond(response); err != nil {
			slog.Warn("auth callout respond failed", "error", err)
		}
	})
	if err != nil {
		slog.Error("auth callout subscribe failed", "error", err)
		os.Exit(1)
	}
	defer sub.Drain() //nolint:errcheck

	metricsAddr := ":" + env("METRICS_PORT", "9100")
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			if nc.IsConnected() {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		if err := http.ListenAndServe(metricsAddr, mux); err != nil { //nolint:gosec
			slog.Error("metrics listener failed", "error", err)
		}
	}()

	slog.Info("nats auth callout serving",
		"subject", natsAuthCalloutSubject,
		"auth_exchange_url", svc.resolver.(*authExchangeSessionResolver).exchangeURL,
		"metrics", metricsAddr,
	)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("nats auth callout shutting down")
}
