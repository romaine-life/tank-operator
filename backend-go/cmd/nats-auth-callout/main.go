package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// natsAuthCalloutSubject is where the NATS server sends authorization
// requests when authorization.auth_callout is configured.
const natsAuthCalloutSubject = "$SYS.REQ.USER.AUTH"

// defaultTokenAudience is the platform service-account token audience used
// by auth.romaine.life's exchange path. NATS auth intentionally validates the
// same audience instead of inventing a parallel NATS-only audience.
const defaultTokenAudience = "https://auth.romaine.life"

var calloutAuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "tank_nats_auth_callout_total",
	Help: "NATS auth-callout outcomes: session (per-session JWT issued), denied_*, error.",
}, []string{"result"})

func recordCalloutAuth(result string) {
	calloutAuthTotal.WithLabelValues(result).Inc()
}

// k8sSessionResolver implements sessionResolver against the cluster:
// TokenReview (audience-pinned) for token→pod, pod labels for pod→session.
type k8sSessionResolver struct {
	client            kubernetes.Interface
	audience          string
	sessionsNamespace string
	serviceAccount    string
}

func (r *k8sSessionResolver) ResolvePodFromToken(ctx context.Context, token string) (string, error) {
	review, err := r.client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{
			Token:     token,
			Audiences: []string{r.audience},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("token review: %w", err)
	}
	if !review.Status.Authenticated {
		return "", fmt.Errorf("token not authenticated: %s", review.Status.Error)
	}
	expected := "system:serviceaccount:" + r.sessionsNamespace + ":" + r.serviceAccount
	if review.Status.User.Username != expected {
		return "", fmt.Errorf("token subject %q is not the session service account", review.Status.User.Username)
	}
	pods := review.Status.User.Extra["authentication.kubernetes.io/pod-name"]
	if len(pods) != 1 || strings.TrimSpace(pods[0]) == "" {
		return "", errors.New("token carries no bound-pod claim (not a projected pod token)")
	}
	return strings.TrimSpace(pods[0]), nil
}

func (r *k8sSessionResolver) SessionStorageKeyForPod(ctx context.Context, podName string) (string, error) {
	pod, err := r.client.CoreV1().Pods(r.sessionsNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get pod: %w", err)
	}
	sessionID := strings.TrimSpace(pod.Labels["tank-operator/session-id"])
	if sessionID == "" {
		return "", errors.New("pod has no tank-operator/session-id label")
	}
	scope := strings.TrimSpace(pod.Labels["tank-operator/session-scope"])
	if scope == "" || scope == "default" {
		return sessionID, nil
	}
	return scope + ":" + sessionID, nil
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

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("in-cluster kubernetes config unavailable", "error", err)
		os.Exit(1)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		slog.Error("kubernetes client init failed", "error", err)
		os.Exit(1)
	}

	svc := &calloutService{
		issuer:  issuer,
		account: natsGlobalAccount,
		resolver: &k8sSessionResolver{
			client:            client,
			audience:          env("NATS_CALLOUT_TOKEN_AUDIENCE", defaultTokenAudience),
			sessionsNamespace: requiredEnv("NATS_CALLOUT_SESSIONS_NAMESPACE"),
			serviceAccount:    env("NATS_CALLOUT_SESSION_SERVICE_ACCOUNT", "claude-session"),
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
		"sessions_namespace", svc.resolver.(*k8sSessionResolver).sessionsNamespace,
		"metrics", metricsAddr,
	)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("nats auth callout shutting down")
}
