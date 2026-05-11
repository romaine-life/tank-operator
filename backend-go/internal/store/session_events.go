package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// SessionEventStore reads the canonical SDK events the pod-side
// agent-runner writes to the `session-events` Cosmos container. The
// orchestrator is a reader only here — the runner owns the write side
// (see agent-runner/src/cosmos.ts). This split is the producer contract:
// one producer, two consumers (DB reader = SPA history-replay,
// WS subscriber = SPA live tap).
//
// Container partition key is /tank_session_id (the small-integer pod id,
// not the SDK's UUID). Doc id is the SDK event uuid (v7, monotonic) so
// the watermark works as a simple string comparison.
type SessionEventStore interface {
	ListBySession(ctx context.Context, tankSessionID, afterUUID string, limit int) ([]map[string]any, error)
}

type cosmosSessionEventStore struct {
	container *azcosmos.ContainerClient
}

func NewCosmosSessionEventStore(endpoint, database, container string, cred azcore.TokenCredential) (SessionEventStore, error) {
	client, err := azcosmos.NewClient(endpoint, cred, nil)
	if err != nil {
		return nil, err
	}
	c, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &cosmosSessionEventStore{container: c}, nil
}

// ListBySession returns events for one session, strictly after the
// given uuid watermark, in ascending uuid order. afterUUID="" returns
// everything from the beginning. limit caps the page size.
//
// Single-partition read (tank_session_id is the partition key) so the
// query is one RU per ~1KB doc — fast and cheap. The /message/* path
// is excluded from indexing (see infra/cosmos.tf) so large assistant
// content doesn't inflate index size; this query doesn't need it
// indexed because we filter on id (the partition's clustering key).
func (s *cosmosSessionEventStore) ListBySession(ctx context.Context, tankSessionID, afterUUID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := "SELECT * FROM c WHERE c.tank_session_id = @sid AND c.id > @after ORDER BY c.id ASC OFFSET 0 LIMIT @limit"
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: tankSessionID},
		{Name: "@after", Value: afterUUID},
		{Name: "@limit", Value: limit},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(tankSessionID), &azcosmos.QueryOptions{QueryParameters: params})
	out := make([]map[string]any, 0, limit)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				continue
			}
			out = append(out, doc)
		}
	}
	// Cosmos should already sort by the ORDER BY clause, but the pager
	// concatenation across pages can return slightly out-of-order rows
	// on the page boundary; an explicit sort is cheap and removes the
	// edge case for the SPA's dedupe-by-uuid path.
	sort.SliceStable(out, func(i, j int) bool {
		ai, _ := out[i]["id"].(string)
		aj, _ := out[j]["id"].(string)
		return ai < aj
	})
	return out, nil
}

// Stub for local dev where Cosmos isn't configured.
type StubSessionEventStore struct{}

func (StubSessionEventStore) ListBySession(_ context.Context, _, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}
