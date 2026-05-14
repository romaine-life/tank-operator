package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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

func TestRequireInternalCallerFallsBackToDefaultServiceAccountAudience(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	var calls int
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		calls++
		review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		switch calls {
		case 1:
			if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "tank-operator" {
				t.Fatalf("first audiences=%#v, want tank-operator audience", review.Spec.Audiences)
			}
			return true, &authv1.TokenReview{
				Status: authv1.TokenReviewStatus{Authenticated: false},
			}, nil
		case 2:
			if len(review.Spec.Audiences) != 0 {
				t.Fatalf("second audiences=%#v, want default Kubernetes token audience", review.Spec.Audiences)
			}
			return true, &authv1.TokenReview{
				Status: authv1.TokenReviewStatus{
					Authenticated: true,
					User: authv1.UserInfo{
						Username: "system:serviceaccount:mcp-glimmung:mcp-glimmung",
					},
				},
			}, nil
		default:
			t.Fatalf("unexpected TokenReview call %d", calls)
			return true, nil, nil
		}
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
	if calls != 2 {
		t.Fatalf("TokenReview calls=%d, want 2", calls)
	}
}
