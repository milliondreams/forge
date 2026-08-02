package agentoscredential

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/secrets"
)

type memoryCredentialStore struct {
	entries map[string]string
}

func (s *memoryCredentialStore) Get(key string) (string, bool) {
	value, ok := s.entries[key]
	return value, ok
}

func (s *memoryCredentialStore) Put(key, value string) error {
	s.entries[key] = value
	return nil
}

func (s *memoryCredentialStore) Delete(key string) (bool, error) {
	_, ok := s.entries[key]
	delete(s.entries, key)
	return ok, nil
}

func (s *memoryCredentialStore) List(prefix string) []string {
	keys := make([]string, 0)
	for key := range s.entries {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func TestProviderResolvesManagedSecret(t *testing.T) {
	provider := NewProvider(&memoryCredentialStore{entries: map[string]string{
		"secret:org|API_KEY": "value",
	}}, nil)
	value, err := provider.Resolve(context.Background(), "secret:org|API_KEY")
	if err != nil || value != "value" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
}

func TestProviderResolvesStoredOAuthTokenWithoutManager(t *testing.T) {
	provider := NewProvider(&memoryCredentialStore{entries: map[string]string{
		"oauth:org|github": `{"access_token":"token"}`,
	}}, nil)
	value, err := provider.Resolve(context.Background(), "oauth:org|github")
	if err != nil || value != "token" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
}

func TestProviderReturnsNotFound(t *testing.T) {
	provider := NewProvider(&memoryCredentialStore{entries: map[string]string{}}, nil)
	if _, err := provider.Resolve(context.Background(), "secret:org|missing"); !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrSecretNotFound", err)
	}
}
