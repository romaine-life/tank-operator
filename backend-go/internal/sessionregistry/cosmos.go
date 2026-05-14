package sessionregistry

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

type CosmosStore struct {
	container *azcosmos.ContainerClient
	scope     string
}

func NewCosmosStore(endpoint, database, container, scope string, credential azcore.TokenCredential) (*CosmosStore, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	client, err := azcosmos.NewClient(endpoint, credential, nil)
	if err != nil {
		return nil, err
	}
	containerClient, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &CosmosStore{container: containerClient, scope: scope}, nil
}

func (s *CosmosStore) List(ctx context.Context, owner string) ([]compat.SessionRecord, error) {
	normalized := strings.ToLower(owner)
	query, parameters := s.query(normalized)
	pager := s.container.NewQueryItemsPager(
		query,
		azcosmos.NewPartitionKeyString(normalized),
		&azcosmos.QueryOptions{QueryParameters: parameters},
	)

	var records []compat.SessionRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			record, err := sessionFromDoc(item)
			if err != nil {
				return nil, err
			}
			if record.Scope == s.scope {
				records = append(records, record)
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt < records[j].CreatedAt
	})
	return records, nil
}

func (s *CosmosStore) query(email string) (string, []azcosmos.QueryParameter) {
	if s.scope == "default" {
		return "SELECT * FROM c WHERE c.email = @email AND c.type = 'session' AND c.visible = true AND (NOT IS_DEFINED(c.session_scope) OR c.session_scope = 'default')",
			[]azcosmos.QueryParameter{{Name: "@email", Value: email}}
	}
	return "SELECT * FROM c WHERE c.email = @email AND c.type = 'session' AND c.visible = true AND c.session_scope = @scope",
		[]azcosmos.QueryParameter{
			{Name: "@email", Value: email},
			{Name: "@scope", Value: s.scope},
		}
}

func sessionFromDoc(data []byte) (compat.SessionRecord, error) {
	var doc struct {
		ID           string  `json:"id"`
		Email        string  `json:"email"`
		Mode         string  `json:"mode"`
		SessionScope string  `json:"session_scope"`
		Scope        string  `json:"scope"`
		SessionID    string  `json:"session_id"`
		PodName      string  `json:"pod_name"`
		Name         *string `json:"name"`
		Visible      *bool   `json:"visible"`
		RequestedAt  string  `json:"requested_at"`
		CreatedAt    string  `json:"created_at"`
		UpdatedAt    string  `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return compat.SessionRecord{}, err
	}
	scope := doc.SessionScope
	if scope == "" {
		scope = doc.Scope
	}
	if scope == "" {
		scope = "default"
	}
	sessionID := doc.SessionID
	if sessionID == "" {
		sessionID = strings.TrimPrefix(doc.ID, "session:")
		if scope != "default" {
			sessionID = strings.TrimPrefix(sessionID, scope+":")
		}
	}
	return compat.SessionRecord{
		ID:          sessionID,
		Email:       doc.Email,
		Mode:        defaultString(doc.Mode, compat.DefaultSessionMode),
		Scope:       scope,
		PodName:     doc.PodName,
		Name:        doc.Name,
		Visible:     doc.Visible == nil || *doc.Visible,
		RequestedAt: doc.RequestedAt,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
