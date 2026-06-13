package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

// stubResolver maps SA tokens to pods and pods to storage keys without a
// cluster: the Kubernetes seam under test elsewhere stays out of the
// authorization-semantics tests.
type stubResolver struct {
	tokens map[string]string // SA token -> pod name
	pods   map[string]string // pod name -> storage key
}

func (s *stubResolver) ResolvePodFromToken(_ context.Context, token string) (string, error) {
	pod, ok := s.tokens[token]
	if !ok {
		return "", errors.New("token rejected")
	}
	return pod, nil
}

func (s *stubResolver) SessionStorageKeyForPod(_ context.Context, podName string) (string, error) {
	key, ok := s.pods[podName]
	if !ok {
		return "", errors.New("no session binding")
	}
	return key, nil
}

const testCalloutPass = "callout-pw"

// startCalloutServer runs an embedded nats-server with auth_callout enabled
// and the callout service subscribed — the REAL authorization path end to
// end: server -> $SYS.REQ.USER.AUTH -> calloutService.Handle -> signed user
// JWT -> server-enforced permissions.
func startCalloutServer(t *testing.T, svc *calloutService) (*server.Server, string) {
	t.Helper()
	issuerPub, err := svc.issuer.PublicKey()
	if err != nil {
		t.Fatalf("issuer public key: %v", err)
	}
	conf := fmt.Sprintf(`
		listen: 127.0.0.1:-1
		authorization {
			users = [
				{ user: "callout", password: "%s" }
			]
			auth_callout {
				issuer: "%s"
				auth_users: [ "callout" ]
			}
		}
	`, testCalloutPass, issuerPub)
	confFile := filepath.Join(t.TempDir(), "nats.conf")
	if err := os.WriteFile(confFile, []byte(conf), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	opts, err := server.ProcessConfigFile(confFile)
	if err != nil {
		t.Fatalf("process conf: %v", err)
	}
	opts.NoLog = true
	opts.NoSigs = true
	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatalf("embedded nats server never became ready")
	}
	t.Cleanup(srv.Shutdown)

	url := srv.ClientURL()
	nc, err := nats.Connect(url, nats.UserInfo("callout", testCalloutPass))
	if err != nil {
		t.Fatalf("callout service connect: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	_, err = nc.QueueSubscribe(natsAuthCalloutSubject, "test-callout", func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		response, err := svc.Handle(ctx, msg.Data)
		if err != nil {
			return
		}
		_ = msg.Respond(response)
	})
	if err != nil {
		t.Fatalf("callout subscribe: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("callout flush: %v", err)
	}
	return srv, url
}

func testService(t *testing.T) *calloutService {
	t.Helper()
	issuer, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	return &calloutService{
		issuer:  issuer,
		account: natsGlobalAccount,
		resolver: &stubResolver{
			tokens: map[string]string{"sa-token-864": "session-pod-864"},
			pods:   map[string]string{"session-pod-864": "864"},
		},
		commandStream: defaultCommandStream,
		providers:     defaultProviders,
		userTTL:       time.Hour,
		now:           time.Now,
	}
}

// permissionErrors registers an error collector: NATS reports publish
// permission violations asynchronously on the offending connection.
func permissionErrors(t *testing.T, nc *nats.Conn) func() []string {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, err.Error())
	})
	return func() []string {
		// One round trip guarantees any violation for already-flushed
		// publishes has arrived.
		if err := nc.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(seen))
		copy(out, seen)
		return out
	}
}

func TestSessionPodGetsOwnSubjectsOnly(t *testing.T) {
	svc := testService(t)
	_, url := startCalloutServer(t, svc)

	nc, err := nats.Connect(url, nats.UserInfo("864", "sa-token-864"))
	if err != nil {
		t.Fatalf("session pod connect: %v", err)
	}
	defer nc.Close()
	errs := permissionErrors(t, nc)

	// Own event subject: allowed.
	if err := nc.Publish(sessionbus.SessionEventSubject("864"), []byte("{}")); err != nil {
		t.Fatalf("publish own events: %v", err)
	}
	// Own command-stream consumer API: allowed.
	durable := "claude_" + sessionbus.ScopeToken("default") + "_" + sessionbus.SessionIDToken("864")
	if err := nc.Publish("$JS.API.CONSUMER.MSG.NEXT."+defaultCommandStream+"."+durable, []byte("{}")); err != nil {
		t.Fatalf("publish own consumer next: %v", err)
	}
	if violations := errs(); len(violations) != 0 {
		t.Fatalf("own-session subjects must be allowed, got violations: %v", violations)
	}

	// Another session's event subject: denied.
	if err := nc.Publish(sessionbus.SessionEventSubject("865"), []byte("{}")); err != nil {
		t.Fatalf("publish queues locally even when denied: %v", err)
	}
	// Another session's durable: denied.
	other := "claude_" + sessionbus.ScopeToken("default") + "_" + sessionbus.SessionIDToken("865")
	if err := nc.Publish("$JS.API.CONSUMER.MSG.NEXT."+defaultCommandStream+"."+other, []byte("{}")); err != nil {
		t.Fatalf("publish queues locally even when denied: %v", err)
	}
	violations := errs()
	if len(violations) != 2 {
		t.Fatalf("cross-session subjects must violate, got: %v", violations)
	}
	for _, v := range violations {
		if !strings.Contains(strings.ToLower(v), "permissions violation") {
			t.Fatalf("expected permissions violation, got: %v", v)
		}
	}
}

func TestSessionPodCannotSubscribeToOtherSubjects(t *testing.T) {
	svc := testService(t)
	_, url := startCalloutServer(t, svc)

	nc, err := nats.Connect(url, nats.UserInfo("864", "sa-token-864"))
	if err != nil {
		t.Fatalf("session pod connect: %v", err)
	}
	defer nc.Close()
	errs := permissionErrors(t, nc)

	// Event-stream wiretap and command snooping are subscription-denied.
	if _, err := nc.SubscribeSync("tank.session.>"); err != nil {
		t.Fatalf("subscribe queues locally: %v", err)
	}
	if _, err := nc.SubscribeSync("tank.cmd.>"); err != nil {
		t.Fatalf("subscribe queues locally: %v", err)
	}
	violations := errs()
	if len(violations) != 2 {
		t.Fatalf("cross-session subscriptions must violate, got: %v", violations)
	}

	// The inbox needed for JS API replies stays allowed.
	if _, err := nc.SubscribeSync(nats.NewInbox()); err != nil {
		t.Fatalf("inbox subscribe: %v", err)
	}
	if violations := errs(); len(violations) != 2 {
		t.Fatalf("inbox subscription must be allowed, got new violations: %v", violations[2:])
	}
}

func TestUnknownCredentialsAreRejected(t *testing.T) {
	svc := testService(t)
	_, url := startCalloutServer(t, svc)

	if _, err := nats.Connect(url, nats.UserInfo("864", "wrong-token")); err == nil {
		t.Fatalf("bad SA token must be rejected at connect")
	}
	if _, err := nats.Connect(url, nats.Token("shared-fleet-token")); err == nil {
		t.Fatalf("bare shared token must be rejected at connect")
	}
	if _, err := nats.Connect(url); err == nil {
		t.Fatalf("credential-less connect must be rejected")
	}
}

func TestClaimedIdentityMustMatchPodBinding(t *testing.T) {
	svc := testService(t)
	_, url := startCalloutServer(t, svc)

	// The token is valid and binds to session 864 — claiming 865 is a
	// mis-wired or hostile pod and must be rejected, not silently
	// downgraded to the claimed session.
	if _, err := nats.Connect(url, nats.UserInfo("865", "sa-token-864")); err == nil {
		t.Fatalf("identity mismatch must be rejected at connect")
	}
}

func TestSessionDurableNamesMirrorRunnerShared(t *testing.T) {
	names := sessionDurableNames("864", []string{"claude", "claude-secondary"})
	scope := sessionbus.ScopeToken("default")
	sid := sessionbus.SessionIDToken("864")
	want := []string{
		"claude_" + scope + "_" + sid,
		"claude_control_" + scope + "_" + sid,
		// runner-shared sanitizeConsumerName folds '-' to '_'.
		"claude_secondary_" + scope + "_" + sid,
		"claude_secondary_control_" + scope + "_" + sid,
	}
	if len(names) != len(want) {
		t.Fatalf("durable names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("durable[%d] = %q, want %q", i, names[i], want[i])
		}
	}
	// Scoped keys split into scope:id.
	scoped := sessionDurableNames("slot1:42", []string{"codex"})
	wantScoped := "codex_" + sessionbus.ScopeToken("slot1") + "_" + sessionbus.SessionIDToken("42")
	if scoped[0] != wantScoped {
		t.Fatalf("scoped durable = %q, want %q", scoped[0], wantScoped)
	}
}
