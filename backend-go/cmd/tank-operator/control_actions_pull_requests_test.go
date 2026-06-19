package main

import (
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// TestSessionPullRequestRefFromControlAction exercises the pure filter +
// ref-builder that decides which control actions become durable PR sightings.
// It is the hinge of the feature: only github.pull_request.* rows carrying a
// real github.com/.../pull/N URL may land on the session's pull_requests
// projection, and the ref must carry exactly what the chip/page render.
func TestSessionPullRequestRefFromControlAction(t *testing.T) {
	created := time.Date(2026, 6, 19, 17, 10, 44, 0, time.UTC)
	const prURL = "https://github.com/romaine-life/tank-operator/pull/1360"

	t.Run("pr open builds the full ref", func(t *testing.T) {
		n := 1360
		ref, ok := sessionPullRequestRefFromControlAction(pgstore.ControlActionEvent{
			Action:    "github.pull_request.open",
			Status:    "succeeded",
			TargetRef: prURL,
			RepoOwner: "romaine-life",
			RepoName:  "tank-operator",
			PRNumber:  &n,
			CreatedAt: created,
		})
		if !ok {
			t.Fatal("expected ok=true for github.pull_request.open with a /pull/ URL")
		}
		if ref.URL != prURL || ref.Repo != "romaine-life/tank-operator" || ref.Number != 1360 ||
			ref.Action != "github.pull_request.open" || ref.Status != "succeeded" ||
			ref.UpdatedAt != "2026-06-19T17:10:44Z" {
			t.Fatalf("ref = %#v", ref)
		}
	})

	t.Run("any github.pull_request.* counts (merge updates the same PR)", func(t *testing.T) {
		ref, ok := sessionPullRequestRefFromControlAction(pgstore.ControlActionEvent{
			Action:    "github.pull_request.merge",
			TargetRef: prURL,
			CreatedAt: created,
		})
		if !ok || ref.URL != prURL || ref.Action != "github.pull_request.merge" {
			t.Fatalf("merge: ok=%v ref=%#v", ok, ref)
		}
		// Missing repo owner/name must yield an empty repo, never a bare "/".
		if ref.Repo != "" {
			t.Fatalf("repo = %q, want empty", ref.Repo)
		}
	})

	t.Run("non-PR actions are skipped", func(t *testing.T) {
		for _, action := range []string{"github.commit.push", "github.commit.ci", "github.break_glass.request", ""} {
			if _, ok := sessionPullRequestRefFromControlAction(pgstore.ControlActionEvent{
				Action:    action,
				TargetRef: prURL,
				CreatedAt: created,
			}); ok {
				t.Fatalf("action %q must not be recorded as a PR sighting", action)
			}
		}
	})

	t.Run("PR action without a github /pull/ URL is skipped", func(t *testing.T) {
		for _, bad := range []string{
			"",
			"https://github.com/romaine-life/tank-operator",
			"https://github.com/romaine-life/tank-operator/issues/5",
			"https://example.com/owner/repo/pull/1",
			"not a url",
		} {
			if _, ok := sessionPullRequestRefFromControlAction(pgstore.ControlActionEvent{
				Action:    "github.pull_request.mergeability",
				TargetRef: bad,
				CreatedAt: created,
			}); ok {
				t.Fatalf("target_ref %q must not produce a PR sighting", bad)
			}
		}
	})

	t.Run("nil pr number leaves number zero", func(t *testing.T) {
		ref, ok := sessionPullRequestRefFromControlAction(pgstore.ControlActionEvent{
			Action:    "github.pull_request.open",
			TargetRef: prURL,
			CreatedAt: created,
		})
		if !ok || ref.Number != 0 {
			t.Fatalf("ok=%v number=%d, want ok=true number=0", ok, ref.Number)
		}
	})
}
