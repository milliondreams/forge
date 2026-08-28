package oauth

import (
	"fmt"
	"sync"
)

// TokenStore persists OAuth token entries keyed by (orgID, providerID).
// Implement this interface to swap in keychain, encrypted DB, or other backends.
type TokenStore interface {
	Save(orgID, providerID string, entry *tokenEntry) error
	Load(orgID, providerID string) (*tokenEntry, bool, error)
	Delete(orgID, providerID string) bool
}

// NewTokenStore creates a TokenStore by name. Supported backends:
//   - "keychain" (default): OS keychain (macOS Keychain, Windows Credential
//     Manager, Linux Secret Service).
//   - "memory": test-only in-process store, tokens lost on restart.
func NewTokenStore(kind string) (TokenStore, error) {
	switch kind {
	case "", "keychain":
		return NewKeychainTokenStore(), nil
	case "memory":
		return NewInMemoryTokenStore(), nil
	default:
		return nil, fmt.Errorf("unknown oauth token store %q; supported: memory, keychain", kind)
	}
}

// InMemoryTokenStore is the default in-process token store. Tokens are lost
// on server restart.
type InMemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]*tokenEntry // key: "StoreKey(orgID, providerID)"
}

func NewInMemoryTokenStore() *InMemoryTokenStore {
	return &InMemoryTokenStore{tokens: make(map[string]*tokenEntry)}
}

func (s *InMemoryTokenStore) Save(orgID, providerID string, entry *tokenEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[StoreKey(orgID, providerID)] = entry
	return nil
}

func (s *InMemoryTokenStore) Load(orgID, providerID string) (*tokenEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[StoreKey(orgID, providerID)]
	return e, ok, nil
}

func (s *InMemoryTokenStore) Delete(orgID, providerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := StoreKey(orgID, providerID)
	_, ok := s.tokens[key]
	delete(s.tokens, key)
	return ok
}
