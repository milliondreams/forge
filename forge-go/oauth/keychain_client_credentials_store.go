package oauth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/zalando/go-keyring"
)

// KeychainClientCredentialsStore persists DCR-issued client credentials in the
// OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service
// via libsecret), namespaced separately from OAuth tokens.
type KeychainClientCredentialsStore struct {
	service string
}

func NewKeychainClientCredentialsStore() *KeychainClientCredentialsStore {
	return NewKeychainClientCredentialsStoreWithService(forgepath.KeychainService())
}

func NewKeychainClientCredentialsStoreWithService(service string) *KeychainClientCredentialsStore {
	return &KeychainClientCredentialsStore{service: service}
}

func (s *KeychainClientCredentialsStore) SaveCredentials(providerID string, c *clientCredentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling client credentials: %w", err)
	}
	if err := keyring.Set(s.service, clientStoreKey(providerID), string(data)); err != nil {
		return fmt.Errorf("saving client credentials to keychain: %w", err)
	}
	return nil
}

func (s *KeychainClientCredentialsStore) LoadCredentials(providerID string) (*clientCredentials, bool, error) {
	data, err := keyring.Get(s.service, clientStoreKey(providerID))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("loading client credentials from keychain: %w", err)
	}
	var c clientCredentials
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, false, fmt.Errorf("parsing client credentials from keychain: %w", err)
	}
	return &c, true, nil
}

func (s *KeychainClientCredentialsStore) DeleteCredentials(providerID string) bool {
	return keyring.Delete(s.service, clientStoreKey(providerID)) == nil
}
