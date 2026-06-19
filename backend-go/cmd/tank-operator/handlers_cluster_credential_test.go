package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// clusterCredentialServer builds an appServer whose fake cluster authenticates
// the session pod (TokenReview), serves its pod, and mints a trusted-SA token.
// restricted controls whether the pod carries TANK_RESTRICTED_GIT=true.
func clusterCredentialServer(t *testing.T, sessionID string, restricted bool) *appServer {
	t.Helper()
	var env []corev1.EnvVar
	if restricted {
		env = []corev1.EnvVar{{Name: "TANK_RESTRICTED_GIT", Value: "true"}}
	}
	k8s := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-" + sessionID,
			Namespace: "tank-operator-sessions",
			UID:       types.UID("pod-uid-" + sessionID),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tank-operator",
				"tank-operator/session-id":     sessionID,
				"tank-operator/session-scope":  "default",
			},
			Annotations: map[string]string{"tank-operator/owner-email": "owner@example.test"},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "claude-session",
			Containers:         []corev1.Container{{Name: "sandbox", Env: env}},
		},
	})
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{
				Authenticated: true,
				User: authv1.UserInfo{
					Username: "system:serviceaccount:tank-operator-sessions:claude-session",
					Extra: map[string]authv1.ExtraValue{
						"authentication.kubernetes.io/pod-name": {"session-" + sessionID},
						"authentication.kubernetes.io/pod-uid":  {"pod-uid-" + sessionID},
					},
				},
			},
		}, nil
	})
	k8s.Fake.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(ktesting.CreateActionImpl)
		if !ok || ca.GetSubresource() != "token" {
			return false, nil, nil
		}
		if ca.Name != "claude-session-trusted" {
			t.Fatalf("CreateToken target = %q, want claude-session-trusted", ca.Name)
		}
		return true, &authv1.TokenRequest{
			Status: authv1.TokenRequestStatus{
				Token:               "minted-trusted-token",
				ExpirationTimestamp: metav1.Now(),
			},
		}, nil
	})
	return &appServer{
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "default",
		sessionServiceAccount: "claude-session",
	}
}

func postClusterCredential(t *testing.T, s *appServer) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/session-cluster-credential", nil)
	req.Header.Set("Authorization", "Bearer pod-token")
	rec := httptest.NewRecorder()
	s.handleInternalClusterCredential(rec, req)
	return rec
}

func TestClusterCredential_NonRestrictedMintsExecCredential(t *testing.T) {
	rec := postClusterCredential(t, clusterCredentialServer(t, "42", false))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Status     struct {
			Token               string `json:"token"`
			ExpirationTimestamp string `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, rec.Body.String())
	}
	if out.APIVersion != "client.authentication.k8s.io/v1" || out.Kind != "ExecCredential" {
		t.Fatalf("unexpected ExecCredential envelope: %+v", out)
	}
	if out.Status.Token != "minted-trusted-token" {
		t.Fatalf("token = %q, want minted-trusted-token", out.Status.Token)
	}
	if out.Status.ExpirationTimestamp == "" {
		t.Fatalf("missing expirationTimestamp")
	}
}

func TestClusterCredential_RestrictedRefused(t *testing.T) {
	rec := postClusterCredential(t, clusterCredentialServer(t, "43", true))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "restricted") {
		t.Fatalf("body should explain the restricted refusal: %s", rec.Body.String())
	}
}
