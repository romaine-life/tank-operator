package glimmung

import (
	"context"
	"fmt"
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

// TestDeployImageToTestSlotNon2xxSurfacesStatusAndDetail pins the deploy error
// contract after the stage-2 cutover: the provision gate only reaches deploy once
// the durable ci_image_available row confirms the image is in the registry, so
// there is no "CI image not ready" pending state to special-case — every non-2xx
// (including a 409) surfaces as a plain `glimmung deploy-image returned <code>`
// error with Glimmung's detail preserved. The previously-special 409 mapping and
// its retryable sentinel were deleted with the gate's image-build polling wait.
func TestDeployImageToTestSlotNon2xxSurfacesStatusAndDetail(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"conflict", http.StatusConflict},
		{"unprocessable", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/exchange"):
					_, _ = w.Write([]byte(`{"token":"gtok"}`))
				case strings.HasSuffix(r.URL.Path, "/v1/test-slots/deploy-image"):
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"detail":"no CI image for commit abc"}`))
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			_, err := newTestDeployClient(srv).DeployImageToTestSlot(context.Background(), "user@romaine.life", DeployImageToTestSlotRequest{Project: "p", GitRef: "abc"})
			if err == nil {
				t.Fatalf("want a non-2xx error for status %d", tc.status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("returned %d", tc.status)) {
				t.Fatalf("err=%v, want the status code surfaced", err)
			}
			if !strings.Contains(err.Error(), "no CI image for commit abc") {
				t.Fatalf("err=%v, want Glimmung's detail preserved in the message", err)
			}
		})
	}
}
