package sessionmodel

import (
	"encoding/json"
	"testing"
)

func TestDecodeSessionPullRequestsRoundTrip(t *testing.T) {
	refs := []SessionPullRequestRef{
		{
			Repo:      "romaine-life/tank-operator",
			Number:    1360,
			URL:       "https://github.com/romaine-life/tank-operator/pull/1360",
			Action:    "github.pull_request.open",
			Status:    "succeeded",
			State:     "clean",
			UpdatedAt: "2026-06-19T00:00:00Z",
		},
		{Number: 7, URL: "https://github.com/romaine-life/spirelens/pull/7"},
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	got := DecodeSessionPullRequests(raw)
	if len(got) != 2 {
		t.Fatalf("decoded %d refs, want 2", len(got))
	}
	if got[0].Repo != "romaine-life/tank-operator" || got[0].Number != 1360 ||
		got[0].URL != "https://github.com/romaine-life/tank-operator/pull/1360" ||
		got[0].Action != "github.pull_request.open" || got[0].Status != "succeeded" ||
		got[0].State != "clean" || got[0].UpdatedAt != "2026-06-19T00:00:00Z" {
		t.Fatalf("ref[0] = %#v", got[0])
	}
	if got[1].URL != "https://github.com/romaine-life/spirelens/pull/7" || got[1].Number != 7 {
		t.Fatalf("ref[1] = %#v", got[1])
	}
}

func TestDecodeSessionPullRequestsEmptyAndMalformed(t *testing.T) {
	cases := map[string][]byte{
		"nil":            nil,
		"empty":          {},
		"json null":      []byte("null"),
		"not an array":   []byte(`{"url":"x"}`),
		"malformed json": []byte(`[{"url":`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// Display-only projection: a bad column must decode to nil, never
			// panic or error, so the session list stays renderable.
			if got := DecodeSessionPullRequests(raw); got != nil {
				t.Fatalf("DecodeSessionPullRequests(%q) = %#v, want nil", name, got)
			}
		})
	}
}

// TestSessionPullRequestRefJSONOmitsEmpties pins the wire shape the SPA reads:
// url is always present; every optional field drops out when empty so an
// untouched session and a freshly-seen PR carry no noise. The json tags here are
// load-bearing — the frontend keys PRs by url.
func TestSessionPullRequestRefJSONOmitsEmpties(t *testing.T) {
	raw, err := json.Marshal(SessionPullRequestRef{URL: "https://github.com/o/r/pull/3"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"url":"https://github.com/o/r/pull/3"}`; got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}
