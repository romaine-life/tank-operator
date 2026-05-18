package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
)

type stubResolver struct {
	keys map[string]*rsa.PublicKey
	err  error
}

func (s stubResolver) PublicKey(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	key, ok := s.keys[kid]
	if !ok {
		return nil, errors.New("kid not found")
	}
	return key, nil
}

func genKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &priv.PublicKey
}

func TestChainedKeyResolverReturnsFirstHit(t *testing.T) {
	keyA := genKey(t)
	first := stubResolver{keys: map[string]*rsa.PublicKey{"kid-a": keyA}}
	second := stubResolver{keys: map[string]*rsa.PublicKey{"kid-b": genKey(t)}}

	chain := NewChainedKeyResolver(first, second)
	got, err := chain.PublicKey(context.Background(), "kid-a")
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if got != keyA {
		t.Errorf("got=%p keyA=%p", got, keyA)
	}
}

func TestChainedKeyResolverFallsThroughOnFirstMiss(t *testing.T) {
	keyB := genKey(t)
	first := stubResolver{keys: map[string]*rsa.PublicKey{"kid-a": genKey(t)}}
	second := stubResolver{keys: map[string]*rsa.PublicKey{"kid-b": keyB}}

	chain := NewChainedKeyResolver(first, second)
	got, err := chain.PublicKey(context.Background(), "kid-b")
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if got != keyB {
		t.Errorf("got=%p keyB=%p", got, keyB)
	}
}

func TestChainedKeyResolverReportsLastErrorOnAllMiss(t *testing.T) {
	first := stubResolver{err: errors.New("alpha-down")}
	second := stubResolver{err: errors.New("beta-down")}

	chain := NewChainedKeyResolver(first, second)
	_, err := chain.PublicKey(context.Background(), "kid-z")
	if err == nil {
		t.Fatal("expected error when no resolver matched")
	}
	if !errContains(err, "beta-down") {
		t.Errorf("err=%v, want wrapping the last (beta-down) failure", err)
	}
}

func TestChainedKeyResolverEmptyConfigErrors(t *testing.T) {
	chain := NewChainedKeyResolver()
	_, err := chain.PublicKey(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error from empty chain")
	}
}

func errContains(err error, substr string) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), substr)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
