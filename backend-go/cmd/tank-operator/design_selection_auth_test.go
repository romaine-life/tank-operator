package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// TestDesignSelectionRequiresAuth pins the bearer gate on both design
// selection routes. The POST writes attacker-controlled JSON into the
// tank-design-selection ConfigMap that agent design flows read back — an
// unauthenticated POST was simultaneously an arbitrary in-cluster
// ConfigMap write and a prompt-injection channel into whatever agent
// consumes /api/design/selection/latest. Same verifier as every other
// protected route; browser callers attach the auth.romaine.life JWT
// (styleguide inspector now uses authedFetch), in-cluster agent callers
// present role=service exchange tokens.
func TestDesignSelectionRequiresAuth(t *testing.T) {
	app := &appServer{
		k8s:                      fake.NewSimpleClientset(),
		verifier:                 auth.NewVerifier(testJWT(t)),
		designSelectionNamespace: "tank-operator",
	}

	t.Run("unauthenticated POST is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/design/selection", strings.NewReader(`{"id":"x"}`))
		resp := httptest.NewRecorder()
		app.handlePostDesignSelection(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.Code)
		}
		app.designSelectionMu.Lock()
		stored := app.latestDesignSelection
		app.designSelectionMu.Unlock()
		if stored != nil {
			t.Fatalf("unauthenticated POST stored a selection: %#v", stored)
		}
	})

	t.Run("unauthenticated GET is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/design/selection/latest", nil)
		resp := httptest.NewRecorder()
		app.handleGetLatestDesignSelection(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.Code)
		}
	})

	t.Run("authenticated round trip works", func(t *testing.T) {
		post := httptest.NewRequest(http.MethodPost, "/api/design/selection", strings.NewReader(`{"id":"button-primary"}`))
		post.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
		postResp := httptest.NewRecorder()
		app.handlePostDesignSelection(postResp, post)
		if postResp.Code != http.StatusOK {
			t.Fatalf("authed POST status = %d body = %s", postResp.Code, postResp.Body.String())
		}

		get := httptest.NewRequest(http.MethodGet, "/api/design/selection/latest", nil)
		get.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
		getResp := httptest.NewRecorder()
		app.handleGetLatestDesignSelection(getResp, get)
		if getResp.Code != http.StatusOK {
			t.Fatalf("authed GET status = %d", getResp.Code)
		}
		if !strings.Contains(getResp.Body.String(), "button-primary") {
			t.Fatalf("authed GET body = %s, want stored selection", getResp.Body.String())
		}
	})
}
