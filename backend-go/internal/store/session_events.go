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
	// New runner events carry tank_order_key (producer write time + local
	// sequence + event id). Older Claude docs use UUIDv7 and old Codex docs
	// used UUIDv4, so fall back through write timestamps before id to avoid
	// reshuffling existing sessions on every history refresh.
	sortSessionEvents(out)
	return out, nil
}

func sortSessionEvents(out []map[string]any) {
	sort.SliceStable(out, func(i, j int) bool {
		ki := eventOrderKey(out[i])
		kj := eventOrderKey(out[j])
		if ki != "" && kj != "" && ki != kj {
			return ki < kj
		}
		if ki != kj {
			return kj == ""
		}
		ti := eventOrderTime(out[i])
		tj := eventOrderTime(out[j])
		if ti != "" && tj != "" && ti != tj {
			return ti < tj
		}
		if ti != tj {
			return tj == ""
		}
		ai, _ := out[i]["id"].(string)
		aj, _ := out[j]["id"].(string)
		return ai < aj
	})
}

func eventOrderKey(doc map[string]any) string {
	if value, ok := doc["tank_order_key"].(string); ok && value != "" {
		return value
	}
	return ""
}

func eventOrderTime(doc map[string]any) string {
	for _, field := range []string{"written_at", "timestamp", "time", "created_at"} {
		if value, ok := doc[field].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// Stub for local dev where Cosmos isn't configured.
type StubSessionEventStore struct{}

func (StubSessionEventStore) ListBySession(_ context.Context, _, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}
