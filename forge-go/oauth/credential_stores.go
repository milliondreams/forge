package oauth

import (
	"encoding/json"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/credentials"
)

type CredentialTokenStore struct{ store credentials.Store }

func NewCredentialTokenStore(store credentials.Store) *CredentialTokenStore {
	return &CredentialTokenStore{store: store}
}

func (s *CredentialTokenStore) Save(orgID, providerID string, entry *tokenEntry) error {
	data, err := json.Marshal(toStoredEntry(entry))
	if err != nil {
		return fmt.Errorf("marshaling token: %w", err)
	}
	return s.store.Put(StoreKey(orgID, providerID), string(data))
}

func (s *CredentialTokenStore) Load(orgID, providerID string) (*tokenEntry, bool) {
	data, ok := s.store.Get(StoreKey(orgID, providerID))
	if !ok {
		return nil, false
	}
	var entry storedEntry
	if json.Unmarshal([]byte(data), &entry) != nil {
		return nil, false
	}
	return fromStoredEntry(&entry), true
}

func (s *CredentialTokenStore) Delete(orgID, providerID string) (bool, error) {
	return s.store.Delete(StoreKey(orgID, providerID))
}

type CredentialClientCredentialsStore struct{ store credentials.Store }

func NewCredentialClientCredentialsStore(store credentials.Store) *CredentialClientCredentialsStore {
	return &CredentialClientCredentialsStore{store: store}
}

func (s *CredentialClientCredentialsStore) SaveCredentials(providerID string, value *clientCredentials) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling client credentials: %w", err)
	}
	return s.store.Put(clientStoreKey(providerID), string(data))
}

func (s *CredentialClientCredentialsStore) LoadCredentials(providerID string) (*clientCredentials, bool) {
	data, ok := s.store.Get(clientStoreKey(providerID))
	if !ok {
		return nil, false
	}
	var value clientCredentials
	if json.Unmarshal([]byte(data), &value) != nil {
		return nil, false
	}
	return &value, true
}

func (s *CredentialClientCredentialsStore) DeleteCredentials(providerID string) (bool, error) {
	return s.store.Delete(clientStoreKey(providerID))
}
