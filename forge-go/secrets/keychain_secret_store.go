package secrets

import (
	"errors"
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/zalando/go-keyring"
)

// KeychainSecretStore persists org-scoped secret values in the OS keychain

type KeychainSecretStore struct {
	service string
}

func NewKeychainSecretStore() *KeychainSecretStore {
	return NewKeychainSecretStoreWithService(forgepath.KeychainService())
}

func NewKeychainSecretStoreWithService(service string) *KeychainSecretStore {
	return &KeychainSecretStore{service: service}
}

func (s *KeychainSecretStore) Save(orgID, name, value string) error {
	return keyring.Set(s.service, SecretStoreKey(orgID, name), value)
}

func (s *KeychainSecretStore) Delete(orgID, name string) (bool, error) {
	err := keyring.Delete(s.service, SecretStoreKey(orgID, name))
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *KeychainSecretStore) Exists(orgID, name string) bool {
	_, err := keyring.Get(s.service, SecretStoreKey(orgID, name))
	return err == nil
}

func (s *KeychainSecretStore) List(orgID string) ([]string, error) {
	availableKeys, err := keyring.ListUsers(s.service)
	if err != nil {
		return nil, err
	}
	// Non-nil so an org with no secrets marshals as [] rather than null; the
	// contract declares "secrets" as a required array.
	filtered := make([]string, 0)
	// using empty string for name since we want to list all secrets saved for the org
	prefix := SecretStoreKey(orgID, "")
	for _, key := range availableKeys {
		if strings.HasPrefix(key, prefix) {
			// Add only the part after the prefix
			stripped := strings.TrimPrefix(key, prefix)
			filtered = append(filtered, stripped)
		}
	}
	sort.Strings(filtered)
	return filtered, nil
}
