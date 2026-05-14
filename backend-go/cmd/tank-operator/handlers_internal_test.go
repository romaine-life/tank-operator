package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

func TestRequireInternalCallerUsesTankOperatorAudience(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "tank-operator" {
			t.Fatalf("audiences=%#v, want tank-operator audience", review.Spec.Audiences)
		}
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{
				Authenticated: true,
				User: authv1.UserInfo{
					Username: "system:serviceaccount:mcp-glimmung:mcp-glimmung",
				},
			},
		}, nil
	})

	handler := requireInternalCaller(
		k8s,
		map[string]string{"mcp-glimmung/mcp-glimmung": "mcp-glimmung"},
	)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/1/test-state", nil)
	req.Header.Set("Authorization", "Bearer caller-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireInternalCallerRejectsDefaultServiceAccountAudience(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "tank-operator" {
			t.Fatalf("audiences=%#v, want tank-operator audience", review.Spec.Audiences)
		}
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{Authenticated: false},
		}, nil
	})

	handler := requireInternalCaller(
		k8s,
		map[string]string{"mcp-glimmung/mcp-glimmung": "mcp-glimmung"},
	)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/1/test-state", nil)
	req.Header.Set("Authorization", "Bearer caller-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInternalGitHubAttestationMintsTankJWT(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	t.Setenv("SUPER_ADMIN_EMAILS", "owner@example.test")
	installationID := int64(987)
	jwtKey, err := auth.NewInMemoryJWT("test-kid")
	if err != nil {
		t.Fatal(err)
	}
	k8s := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-12",
			Namespace: "tank-operator-sessions",
			UID:       types.UID("pod-uid-12"),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tank-operator",
				"tank-operator/session-id":     "12",
				"tank-operator/session-scope":  "slot-a",
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": "owner@example.test",
			},
		},
		Spec: corev1.PodSpec{ServiceAccountName: "claude-session"},
	})
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "tank-operator" {
			t.Fatalf("audiences=%#v, want tank-operator audience", review.Spec.Audiences)
		}
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{
				Authenticated: true,
				User: authv1.UserInfo{
					Username: "system:serviceaccount:tank-operator-sessions:claude-session",
					Extra: map[string]authv1.ExtraValue{
						"authentication.kubernetes.io/pod-name": {"session-12"},
						"authentication.kubernetes.io/pod-uid":  {"pod-uid-12"},
					},
				},
			},
		}, nil
	})
	server := &appServer{
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "slot-a",
		sessionServiceAccount: "claude-session",
		profiles: testProfilesStore{
			"owner@example.test": {InstallationID: &installationID},
		},
		minter: auth.NewMinter(jwtKey, jwtKey, "owner@example.test"),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/internal/github/attestation", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalGitHubAttestation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" || body.ExpiresAt == "" {
		t.Fatalf("response = %#v", body)
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(body.Token, claims, func(token *jwt.Token) (any, error) {
		return jwtKey.PublicKey(context.Background(), "test-kid")
	}, jwt.WithAudience(auth.GitHubMCPAttestationAudience), jwt.WithIssuer("tank-operator"))
	if err != nil || !parsed.Valid {
		t.Fatalf("attestation did not verify: token=%v err=%v", parsed, err)
	}
	if got, want := claims["owner_email"], "owner@example.test"; got != want {
		t.Fatalf("owner_email = %v, want %q", got, want)
	}
	if got, want := claims["github_installation_id"], float64(987); got != want {
		t.Fatalf("github_installation_id = %v, want %v", got, want)
	}
	if got, want := claims["session_scope"], "slot-a"; got != want {
		t.Fatalf("session_scope = %v, want %q", got, want)
	}
}

func TestHandleInternalGitHubAttestationRejectsUnboundSAToken(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("test-kid")
	if err != nil {
		t.Fatal(err)
	}
	k8s := fake.NewSimpleClientset()
	k8s.Fake.PrependReactor("create", "tokenreviews", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{
				Authenticated: true,
				User: authv1.UserInfo{
					Username: "system:serviceaccount:tank-operator-sessions:claude-session",
				},
			},
		}, nil
	})
	server := &appServer{
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "slot-a",
		sessionServiceAccount: "claude-session",
		profiles:              testProfilesStore{},
		minter:                auth.NewMinter(jwtKey, jwtKey, ""),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/internal/github/attestation", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalGitHubAttestation(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type testProfilesStore map[string]profiles.Profile

func (s testProfilesStore) GetOrCreate(_ context.Context, email string) (profiles.Profile, error) {
	if profile, ok := s[email]; ok {
		return profile, nil
	}
	return profiles.Profile{Email: email}, nil
}
