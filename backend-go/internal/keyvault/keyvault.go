// Package keyvault provides a thin wrapper around Azure Key Vault secrets.
package keyvault

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// SetSecret writes (or updates) a secret in Azure Key Vault.
func SetSecret(ctx context.Context, vaultURL, name, value string, cred azcore.TokenCredential) error {
	client, err := azsecrets.NewClient(vaultURL, cred, nil)
	if err != nil {
		return fmt.Errorf("keyvault client: %w", err)
	}
	_, err = client.SetSecret(ctx, name, azsecrets.SetSecretParameters{
		Value: &value,
	}, nil)
	if err != nil {
		return fmt.Errorf("keyvault set secret %q: %w", name, err)
	}
	return nil
}

// GetSecret reads a secret value from Azure Key Vault.
func GetSecret(ctx context.Context, vaultURL, name string, cred azcore.TokenCredential) (string, error) {
	client, err := azsecrets.NewClient(vaultURL, cred, nil)
	if err != nil {
		return "", fmt.Errorf("keyvault client: %w", err)
	}
	resp, err := client.GetSecret(ctx, name, "", nil)
	if err != nil {
		return "", fmt.Errorf("keyvault get secret %q: %w", name, err)
	}
	if resp.Value == nil {
		return "", fmt.Errorf("keyvault secret %q has nil value", name)
	}
	return *resp.Value, nil
}
