package secrets

import (
	"strings"

	"github.com/rustic-ai/forge/forge-go/credentials"
)

type CredentialSecretStore struct{ store credentials.Store }

func NewCredentialSecretStore(store credentials.Store) *CredentialSecretStore {
	return &CredentialSecretStore{store: store}
}

func (s *CredentialSecretStore) Save(orgID, name, value string) error {
	return s.store.Put(SecretStoreKey(orgID, name), value)
}

func (s *CredentialSecretStore) Delete(orgID, name string) (bool, error) {
	return s.store.Delete(SecretStoreKey(orgID, name))
}

func (s *CredentialSecretStore) Exists(orgID, name string) bool {
	_, ok := s.store.Get(SecretStoreKey(orgID, name))
	return ok
}

func (s *CredentialSecretStore) List(orgID string) ([]string, error) {
	prefix := SecretStoreKey(orgID, "")
	keys := s.store.List(prefix)
	for index := range keys {
		keys[index] = strings.TrimPrefix(keys[index], prefix)
	}
	return keys, nil
}
