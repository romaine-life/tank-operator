package main

import (
	"context"
	"errors"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDefaultTokenAudienceMatchesPlatformAudience(t *testing.T) {
	if got, want := defaultTokenAudience, "https://auth.romaine.life"; got != want {
		t.Fatalf("defaultTokenAudience = %q, want %q", got, want)
	}
}

func TestK8sSessionResolverProductionAuthority(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tank-operator-sessions",
			Name:      "session-908",
			Labels: map[string]string{
				"tank-operator/session-id":    "908",
				"tank-operator/session-scope": defaultSessionScope,
			},
		},
	})
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-sessions:claude-session", "session-908")
	resolver := testK8sResolver(client)

	pod, err := resolver.ResolvePodFromToken(context.Background(), "prod-token")
	if err != nil {
		t.Fatalf("ResolvePodFromToken: %v", err)
	}
	if pod.Namespace != "tank-operator-sessions" || pod.Name != "session-908" || pod.ExpectedScope != defaultSessionScope {
		t.Fatalf("pod ref = %#v", pod)
	}
	key, err := resolver.SessionStorageKeyForPod(context.Background(), pod)
	if err != nil {
		t.Fatalf("SessionStorageKeyForPod: %v", err)
	}
	if key != "908" {
		t.Fatalf("storage key = %q, want 908", key)
	}
}

func TestK8sSessionResolverGlimmungSlotAuthority(t *testing.T) {
	client := fake.NewSimpleClientset(
		trustedSlotNamespace("tank-operator-slot-1"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tank-operator-slot-1-sessions",
				Name:      "session-175",
				Labels: map[string]string{
					"tank-operator/session-id":    "175",
					"tank-operator/session-scope": "tank-operator-slot-1",
				},
			},
		},
	)
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-slot-1-sessions:tank-operator-slot-1-session", "session-175")
	resolver := testK8sResolver(client)

	pod, err := resolver.ResolvePodFromToken(context.Background(), "slot-token")
	if err != nil {
		t.Fatalf("ResolvePodFromToken: %v", err)
	}
	if pod.Namespace != "tank-operator-slot-1-sessions" || pod.Name != "session-175" || pod.ExpectedScope != "tank-operator-slot-1" {
		t.Fatalf("pod ref = %#v", pod)
	}
	key, err := resolver.SessionStorageKeyForPod(context.Background(), pod)
	if err != nil {
		t.Fatalf("SessionStorageKeyForPod: %v", err)
	}
	if key != "tank-operator-slot-1:175" {
		t.Fatalf("storage key = %q, want tank-operator-slot-1:175", key)
	}
}

func TestK8sSessionResolverRejectsUntrustedSlotNamespace(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tank-operator-slot-1-sessions",
		},
	})
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-slot-1-sessions:tank-operator-slot-1-session", "session-175")
	resolver := testK8sResolver(client)

	_, err := resolver.ResolvePodFromToken(context.Background(), "slot-token")
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestK8sSessionResolverRejectsWrongSlotServiceAccount(t *testing.T) {
	client := fake.NewSimpleClientset(trustedSlotNamespace("tank-operator-slot-1"))
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-slot-1-sessions:claude-session", "session-175")
	resolver := testK8sResolver(client)

	_, err := resolver.ResolvePodFromToken(context.Background(), "slot-token")
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestK8sSessionResolverRejectsNonSlotShapedNamespace(t *testing.T) {
	client := fake.NewSimpleClientset(trustedSlotNamespace("tank-operator-preview"))
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-preview-sessions:tank-operator-preview-session", "session-175")
	resolver := testK8sResolver(client)

	_, err := resolver.ResolvePodFromToken(context.Background(), "slot-token")
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestK8sSessionResolverRejectsPodScopeMismatch(t *testing.T) {
	client := fake.NewSimpleClientset(
		trustedSlotNamespace("tank-operator-slot-1"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tank-operator-slot-1-sessions",
				Name:      "session-175",
				Labels: map[string]string{
					"tank-operator/session-id":    "175",
					"tank-operator/session-scope": "tank-operator-slot-2",
				},
			},
		},
	)
	tokenReviewSubject(client, "system:serviceaccount:tank-operator-slot-1-sessions:tank-operator-slot-1-session", "session-175")
	resolver := testK8sResolver(client)

	pod, err := resolver.ResolvePodFromToken(context.Background(), "slot-token")
	if err != nil {
		t.Fatalf("ResolvePodFromToken: %v", err)
	}
	_, err = resolver.SessionStorageKeyForPod(context.Background(), pod)
	assertDenyResult(t, err, "denied_pod_scope_mismatch")
}

func TestK8sSessionResolverRejectsNonServiceAccountSubject(t *testing.T) {
	client := fake.NewSimpleClientset()
	tokenReviewSubject(client, "system:node:node-1", "session-175")
	resolver := testK8sResolver(client)

	_, err := resolver.ResolvePodFromToken(context.Background(), "node-token")
	assertDenyResult(t, err, "denied_subject_invalid")
}

func testK8sResolver(client *fake.Clientset) *k8sSessionResolver {
	return &k8sSessionResolver{
		client:            client,
		audience:          defaultTokenAudience,
		sessionsNamespace: "tank-operator-sessions",
		serviceAccount:    "claude-session",
	}
}

func tokenReviewSubject(client *fake.Clientset, username string, podName string) {
	client.Fake.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenReview{
			Status: authnv1.TokenReviewStatus{
				Authenticated: true,
				User: authnv1.UserInfo{
					Username: username,
					Extra: map[string]authnv1.ExtraValue{
						"authentication.kubernetes.io/pod-name": {podName},
					},
				},
			},
		}, nil
	})
}

func trustedSlotNamespace(slotName string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: slotName + "-sessions",
			Labels: map[string]string{
				glimmungTestSlotLabel:     "true",
				glimmungProjectLabel:      tankOperatorProjectName,
				glimmungNativeSlotNameKey: slotName,
			},
		},
	}
}

func assertDenyResult(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", want)
	}
	var denied calloutDenyError
	if !errors.As(err, &denied) {
		t.Fatalf("error %v is not calloutDenyError", err)
	}
	if denied.result != want {
		t.Fatalf("deny result = %q, want %q (err=%v)", denied.result, want, err)
	}
}
