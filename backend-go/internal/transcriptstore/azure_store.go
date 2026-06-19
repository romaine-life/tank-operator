package transcriptstore

import (
	"context"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

// AzureStore writes transcript snapshots to a private Azure Blob container
// using the orchestrator's workload identity (the same credential shape as
// internal/avatarassets). No SAS, no account key.
type AzureStore struct {
	client    *azblob.Client
	container string
}

func NewAzureStore(accountURL, container string, cred azcore.TokenCredential) (*AzureStore, error) {
	client, err := azblob.NewClient(strings.TrimRight(accountURL, "/"), cred, nil)
	if err != nil {
		return nil, err
	}
	return &AzureStore{client: client, container: strings.TrimSpace(container)}, nil
}

func (s *AzureStore) Put(ctx context.Context, key string, snap Snapshot) error {
	opts := &azblob.UploadBufferOptions{}
	if ct := strings.TrimSpace(snap.ContentType); ct != "" {
		opts.HTTPHeaders = &blob.HTTPHeaders{BlobContentType: &ct}
	}
	if len(snap.Metadata) > 0 {
		md := make(map[string]*string, len(snap.Metadata))
		for k, v := range snap.Metadata {
			value := v
			md[k] = &value
		}
		opts.Metadata = md
	}
	_, err := s.client.UploadBuffer(ctx, s.container, key, snap.Bytes, opts)
	return err
}

func (s *AzureStore) Get(ctx context.Context, key string) (Snapshot, bool, error) {
	resp, err := s.client.DownloadStream(ctx, s.container, key, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, false, err
	}
	snap := Snapshot{Bytes: body}
	if resp.ContentType != nil {
		snap.ContentType = *resp.ContentType
	}
	if len(resp.Metadata) > 0 {
		md := make(map[string]string, len(resp.Metadata))
		for k, v := range resp.Metadata {
			if v != nil {
				md[k] = *v
			}
		}
		snap.Metadata = md
	}
	return snap, true, nil
}
