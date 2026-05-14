package auth

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"path"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	"github.com/golang-jwt/jwt/v5"
)

// currentKIDTTL controls how stale the cached "what version is current"
// answer can be. After rotation, mints lag by at most this duration before
// switching to the new version. Verification is unaffected (verifier
// resolves whichever kid the JWT carries, regardless of "current").
const currentKIDTTL = 60 * time.Second

// KeyVaultJWT signs and verifies session JWTs against an Azure Key Vault Key.
// The private key never leaves KV — minting is a remote `Sign` call; verifying
// fetches the public key (cached per kid) and validates in-process.
type KeyVaultJWT struct {
	client  *azkeys.Client
	keyName string

	currentMu     sync.Mutex
	currentKID    string
	currentExpiry time.Time

	keysMu sync.RWMutex
	keys   map[string]*rsa.PublicKey
}

// NewKeyVaultJWT constructs a JWT signer/resolver backed by `<keyName>` in the
// vault at `vaultURL`. Both Signer and KeyResolver are satisfied by the
// returned value; tests use the InMemoryJWT impl below.
func NewKeyVaultJWT(vaultURL, keyName string, cred azcore.TokenCredential) (*KeyVaultJWT, error) {
	client, err := azkeys.NewClient(vaultURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azkeys client: %w", err)
	}
	return &KeyVaultJWT{
		client:  client,
		keyName: keyName,
		keys:    map[string]*rsa.PublicKey{},
	}, nil
}

// MintJWT builds an RS256 JWT from claims, signs it remotely in Key Vault.
func (j *KeyVaultJWT) MintJWT(ctx context.Context, claims jwt.MapClaims) (string, error) {
	kid, err := j.currentKIDCached(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve current kid: %w", err)
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64URL(headerJSON) + "." + b64URL(payloadJSON)
	digest := sha256.Sum256([]byte(signingInput))

	resp, err := j.client.Sign(ctx, j.keyName, kid, azkeys.SignParameters{
		Algorithm: to.Ptr(azkeys.SignatureAlgorithmRS256),
		Value:     digest[:],
	}, nil)
	if err != nil {
		return "", fmt.Errorf("kv sign: %w", err)
	}
	if resp.Result == nil {
		return "", fmt.Errorf("kv sign: empty signature")
	}
	return signingInput + "." + b64URL(resp.Result), nil
}

// PublicKey resolves a kid (= KV key version) to its RSA public key, caching
// the result indefinitely. Versions are immutable in KV, so a hit on the
// cache is always correct; misses fall through to a GetKey call.
func (j *KeyVaultJWT) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("missing kid")
	}
	j.keysMu.RLock()
	if k, ok := j.keys[kid]; ok {
		j.keysMu.RUnlock()
		return k, nil
	}
	j.keysMu.RUnlock()

	resp, err := j.client.GetKey(ctx, j.keyName, kid, nil)
	if err != nil {
		return nil, fmt.Errorf("kv get key: %w", err)
	}
	if resp.Key == nil {
		return nil, fmt.Errorf("kv get key: empty key bundle")
	}
	pub, err := jwkToRSAPublic(resp.Key)
	if err != nil {
		return nil, err
	}

	j.keysMu.Lock()
	j.keys[kid] = pub
	j.keysMu.Unlock()
	return pub, nil
}

func (j *KeyVaultJWT) CurrentJWK(ctx context.Context) (JWK, error) {
	kid, err := j.currentKIDCached(ctx)
	if err != nil {
		return JWK{}, err
	}
	pub, err := j.PublicKey(ctx, kid)
	if err != nil {
		return JWK{}, err
	}
	return rsaPublicJWK(kid, pub), nil
}

func (j *KeyVaultJWT) currentKIDCached(ctx context.Context) (string, error) {
	j.currentMu.Lock()
	defer j.currentMu.Unlock()
	if j.currentKID != "" && time.Now().Before(j.currentExpiry) {
		return j.currentKID, nil
	}
	resp, err := j.client.GetKey(ctx, j.keyName, "", nil)
	if err != nil {
		return "", fmt.Errorf("kv get current key: %w", err)
	}
	if resp.Key == nil || resp.Key.KID == nil {
		return "", fmt.Errorf("kv get current key: missing KID")
	}
	// KID is a URL like https://vault.vault.azure.net/keys/<name>/<version>.
	// The version is the last path segment and is what callers pass to
	// Sign/GetKey to pin a specific key generation.
	kid := path.Base(string(*resp.Key.KID))
	if kid == "" {
		return "", fmt.Errorf("kv get current key: kid has no version segment: %q", string(*resp.Key.KID))
	}
	j.currentKID = kid
	j.currentExpiry = time.Now().Add(currentKIDTTL)
	// Warm the verification cache too — saves the verifier from a roundtrip
	// the first time it sees this kid.
	if pub, err := jwkToRSAPublic(resp.Key); err == nil {
		j.keysMu.Lock()
		j.keys[kid] = pub
		j.keysMu.Unlock()
	}
	return kid, nil
}

func jwkToRSAPublic(jwk *azkeys.JSONWebKey) (*rsa.PublicKey, error) {
	if jwk == nil {
		return nil, fmt.Errorf("nil JWK")
	}
	if jwk.Kty == nil || (*jwk.Kty != azkeys.KeyTypeRSA && *jwk.Kty != azkeys.KeyTypeRSAHSM) {
		return nil, fmt.Errorf("unsupported key type %v", jwk.Kty)
	}
	if len(jwk.N) == 0 || len(jwk.E) == 0 {
		return nil, fmt.Errorf("JWK missing modulus or exponent")
	}
	n := new(big.Int).SetBytes(jwk.N)
	e := 0
	for _, b := range jwk.E {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("JWK exponent is zero")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
