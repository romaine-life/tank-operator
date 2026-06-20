package glimmung

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestDeployClient(srv *httptest.Server) *Client {
	return &Client{
		http:      srv.Client(),
		baseURL:   srv.URL,
		exchange:  srv.URL + "/exchange",
		saPath:    "ignored",
		readToken: func(string) (string, error) { return "sa-token", nil },
	}
}

// TestDeployImageToTestSlotReturnsErrCIImagePendingOn409 pins the retry contract
// with Glimmung: a 409 (CI image not built yet) surfaces as the typed, retryable
// ErrCIImagePending so the provision gate waits instead of failing.
func TestDeployImageToTestSlotReturnsErrCIImagePendingOn409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exchange"):
			_, _ = w.Write([]byte(`{"token":"gtok"}`))
		case strings.HasSuffix(r.URL.Path, "/v1/test-slots/deploy-image"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"resolve commit sha to CI image: CI image for commit abc is not ready yet; retry once the build completes"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, err := newTestDeployClient(srv).DeployImageToTestSlot(context.Background(), "user@romaine.life", DeployImageToTestSlotRequest{Project: "p", GitRef: "abc"})
	if !errors.Is(err, ErrCIImagePending) {
		t.Fatalf("err=%v, want ErrCIImagePending", err)
	}
	if !strings.Contains(err.Error(), "not ready yet") {
		t.Fatalf("err=%v, want Glimmung's detail preserved in the message", err)
	}
}

// TestDeployImageToTestSlotNon409StaysGenericError: a 422 (genuinely unresolvable
// commit) must NOT be treated as retryable.
func TestDeployImageToTestSlotNon409StaysGenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exchange"):
			_, _ = w.Write([]byte(`{"token":"gtok"}`))
		case strings.HasSuffix(r.URL.Path, "/v1/test-slots/deploy-image"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"no CI image for commit abc"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, err := newTestDeployClient(srv).DeployImageToTestSlot(context.Background(), "user@romaine.life", DeployImageToTestSlotRequest{Project: "p", GitRef: "abc"})
	if errors.Is(err, ErrCIImagePending) {
		t.Fatalf("a 422 must not be ErrCIImagePending: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "returned 422") {
		t.Fatalf("err=%v, want a generic non-2xx error", err)
	}
}
