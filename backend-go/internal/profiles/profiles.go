package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

type Profile struct {
	Email          string  `json:"email"`
	GitHubLogin    *string `json:"github_login"`
	InstallationID *int64  `json:"installation_id"`
}

type Store interface {
	GetOrCreate(ctx context.Context, email string) (Profile, error)
}

type CosmosStore struct {
	container *azcosmos.ContainerClient
}

func NewCosmosStore(endpoint, database, container string, credential azcore.TokenCredential) (*CosmosStore, error) {
	client, err := azcosmos.NewClient(endpoint, credential, nil)
	if err != nil {
		return nil, err
	}
	containerClient, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &CosmosStore{container: containerClient}, nil
}

func (s *CosmosStore) GetOrCreate(ctx context.Context, email string) (Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return Profile{}, nil
	}
	response, err := s.container.ReadItem(ctx, azcosmos.NewPartitionKeyString(normalized), normalized, nil)
	if err != nil {
		var responseErr *azcore.ResponseError
		if errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound {
			return Profile{Email: normalized}, nil
		}
		return Profile{}, err
	}
	profile, err := profileFromDoc(response.Value)
	if err != nil {
		return Profile{}, err
	}
	if profile.Email == "" {
		profile.Email = normalized
	}
	return profile, nil
}

type StubStore struct{}

func (StubStore) GetOrCreate(_ context.Context, email string) (Profile, error) {
	return Profile{Email: strings.ToLower(strings.TrimSpace(email))}, nil
}

func profileFromDoc(data []byte) (Profile, error) {
	var doc struct {
		Email          string  `json:"email"`
		GitHubLogin    *string `json:"github_login"`
		InstallationID *int64  `json:"installation_id"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Profile{}, err
	}
	return Profile{
		Email:          doc.Email,
		GitHubLogin:    doc.GitHubLogin,
		InstallationID: doc.InstallationID,
	}, nil
}
