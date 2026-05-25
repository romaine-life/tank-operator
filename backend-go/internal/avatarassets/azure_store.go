package avatarassets

import (
	"context"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

type AzureImageStore struct {
	client    *azblob.Client
	container string
}

func NewAzureImageStore(accountURL, container string, cred azcore.TokenCredential) (*AzureImageStore, error) {
	client, err := azblob.NewClient(strings.TrimRight(accountURL, "/"), cred, nil)
	if err != nil {
		return nil, err
	}
	return &AzureImageStore{client: client, container: strings.TrimSpace(container)}, nil
}

func (s *AzureImageStore) Put(ctx context.Context, key string, img Image) error {
	_, err := s.client.UploadBuffer(ctx, s.container, key, img.Bytes, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &img.MIME},
	})
	return mapAzureAvatarError(err)
}

func (s *AzureImageStore) Get(ctx context.Context, key string) (Image, error) {
	resp, err := s.client.DownloadStream(ctx, s.container, key, nil)
	if err != nil {
		return Image{}, mapAzureAvatarError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Image{}, err
	}
	mime := ""
	if resp.ContentType != nil {
		mime = *resp.ContentType
	}
	return Image{MIME: mime, Bytes: body}, nil
}

func (s *AzureImageStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteBlob(ctx, s.container, key, nil)
	return mapAzureAvatarError(err)
}

func mapAzureAvatarError(err error) error {
	if err == nil {
		return nil
	}
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return ErrNotFound
	}
	return err
}
