package pgstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CIImageAvailable is one durable "the deployable image for this commit now
// exists in the registry" record (docs/event-driven-rollout.md, deferred
// "deploy / image" hop). It is written by the public POST /webhooks/acr
// receiver (cmd/tank-operator/acr_webhook.go) the instant Azure ACR fires a
// `push` webhook for a `sha-<commit>` image tag, and is the event-driven
// replacement for the test-slot provisioning gate's image-build polling wait.
//
// Identity is (Registry, RepoName, CommitSHA): one row per commit per ACR
// repository, so a re-delivery of the same push refreshes the tag/digest/
// observed_at instead of duplicating. RepoName is the ACR repository (e.g.
// "chess-tactics"), CommitSHA is parsed from the `sha-<commit>` tag, and
// ImageTag is the full tag.
type CIImageAvailable struct {
	Registry    string
	RepoName    string
	CommitSHA   string
	ImageTag    string
	ImageDigest string
	ObservedAt  time.Time
}

// CIImageAvailableStore is the durable image-readiness signal store. It has no
// session scope: an available image is keyed by registry + ACR repository +
// commit, not by an owning session (mirrors DeploymentImageVersionStore, the
// other registry-shaped, pool-only store).
type CIImageAvailableStore struct {
	pool *pgxpool.Pool
}

func NewCIImageAvailableStore(pool *pgxpool.Pool) *CIImageAvailableStore {
	return &CIImageAvailableStore{pool: pool}
}

// ciImageAvailableColumns is the canonical column order shared by every
// RETURNING/SELECT in this store and by scanCIImageAvailable.
const ciImageAvailableColumns = `registry, repo_name, commit_sha, image_tag, image_digest, observed_at`

// UpsertCIImageAvailable records (or refreshes) the image-readiness signal for a
// commit. ON CONFLICT (registry, repo_name, commit_sha) refreshes the tag,
// digest, and observed_at, so the write is idempotent and safe to re-run on
// ACR's at-least-once redelivery of the same push.
func (s *CIImageAvailableStore) UpsertCIImageAvailable(ctx context.Context, rec CIImageAvailable) error {
	if s == nil || s.pool == nil {
		return errors.New("ci image available store unavailable")
	}
	registry := strings.TrimSpace(rec.Registry)
	repoName := strings.TrimSpace(rec.RepoName)
	commitSHA := strings.TrimSpace(rec.CommitSHA)
	if registry == "" || repoName == "" || commitSHA == "" {
		return errors.New("missing registry/repo_name/commit_sha")
	}
	const q = `
		INSERT INTO ci_image_available (
			registry, repo_name, commit_sha, image_tag, image_digest, observed_at
		) VALUES (
			$1, $2, $3, $4, $5, now()
		)
		ON CONFLICT (registry, repo_name, commit_sha) DO UPDATE
		SET image_tag = EXCLUDED.image_tag,
			image_digest = EXCLUDED.image_digest,
			observed_at = now()`
	_, err := s.pool.Exec(ctx, q,
		registry, repoName, commitSHA, strings.TrimSpace(rec.ImageTag), strings.TrimSpace(rec.ImageDigest),
	)
	return err
}

// ImageAvailableForCommit reports whether the deployable image for a commit has
// been observed in the registry. This is the stage-2 consumer's existence
// check — the event-driven replacement for the provisioning gate's image-build
// poll. It is currently unreferenced by production code on purpose: stage 1 only
// records the signal; the gate cutover that reads it is a later stage. Exercised
// now by TestCIImageAvailableUpsertIdempotency so it does not rot before then.
func (s *CIImageAvailableStore) ImageAvailableForCommit(ctx context.Context, registry, repoName, commitSHA string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("ci image available store unavailable")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ci_image_available
			WHERE registry = $1 AND repo_name = $2 AND commit_sha = $3
		)
	`, strings.TrimSpace(registry), strings.TrimSpace(repoName), strings.TrimSpace(commitSHA)).Scan(&exists)
	return exists, err
}

// Get returns a single image-readiness record by its identity (test/debug
// surface; mirrors the other stores' Get).
func (s *CIImageAvailableStore) Get(ctx context.Context, registry, repoName, commitSHA string) (CIImageAvailable, error) {
	if s == nil || s.pool == nil {
		return CIImageAvailable{}, errors.New("ci image available store unavailable")
	}
	const q = `SELECT ` + ciImageAvailableColumns + `
		FROM ci_image_available
		WHERE registry = $1 AND repo_name = $2 AND commit_sha = $3`
	return scanCIImageAvailable(s.pool.QueryRow(ctx, q,
		strings.TrimSpace(registry), strings.TrimSpace(repoName), strings.TrimSpace(commitSHA)))
}

type ciImageAvailableScanner interface {
	Scan(dest ...any) error
}

func scanCIImageAvailable(row ciImageAvailableScanner) (CIImageAvailable, error) {
	var out CIImageAvailable
	err := row.Scan(
		&out.Registry,
		&out.RepoName,
		&out.CommitSHA,
		&out.ImageTag,
		&out.ImageDigest,
		&out.ObservedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CIImageAvailable{}, err
	}
	if err != nil {
		return CIImageAvailable{}, err
	}
	return out, nil
}
