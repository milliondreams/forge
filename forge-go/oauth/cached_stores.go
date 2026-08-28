package oauth

import "sync"

// CachedTokenStore keeps complete token records in memory while preserving the
// keychain as the durable source. Expiry is still evaluated by Manager on every
// GetAccessToken call.
type CachedTokenStore struct {
	store TokenStore
	mu    sync.RWMutex
	items map[string]*tokenEntry
}

func NewCachedTokenStore(store TokenStore) *CachedTokenStore {
	return &CachedTokenStore{store: store, items: make(map[string]*tokenEntry)}
}

func cloneTokenEntry(entry *tokenEntry) *tokenEntry {
	if entry == nil {
		return nil
	}
	return fromStoredEntry(toStoredEntry(entry))
}

func (s *CachedTokenStore) Save(orgID, providerID string, entry *tokenEntry) error {
	if err := s.store.Save(orgID, providerID, entry); err != nil {
		return err
	}
	s.mu.Lock()
	s.items[StoreKey(orgID, providerID)] = cloneTokenEntry(entry)
	s.mu.Unlock()
	return nil
}

func (s *CachedTokenStore) Load(orgID, providerID string) (*tokenEntry, bool, error) {
	key := StoreKey(orgID, providerID)
	s.mu.RLock()
	entry, ok := s.items[key]
	s.mu.RUnlock()
	if ok {
		return cloneTokenEntry(entry), true, nil
	}
	entry, ok, err := s.store.Load(orgID, providerID)
	if err != nil || !ok {
		return nil, false, err
	}
	s.mu.Lock()
	s.items[key] = cloneTokenEntry(entry)
	s.mu.Unlock()
	return cloneTokenEntry(entry), true, nil
}

func (s *CachedTokenStore) Delete(orgID, providerID string) bool {
	deleted := s.store.Delete(orgID, providerID)
	s.mu.Lock()
	delete(s.items, StoreKey(orgID, providerID))
	s.mu.Unlock()
	return deleted
}

func (s *CachedTokenStore) Clear() {
	s.mu.Lock()
	clear(s.items)
	s.mu.Unlock()
}

type CachedClientCredentialsStore struct {
	store ClientCredentialsStore
	mu    sync.RWMutex
	items map[string]*clientCredentials
}

func NewCachedClientCredentialsStore(store ClientCredentialsStore) *CachedClientCredentialsStore {
	return &CachedClientCredentialsStore{store: store, items: make(map[string]*clientCredentials)}
}

func cloneClientCredentials(c *clientCredentials) *clientCredentials {
	if c == nil {
		return nil
	}
	copy := *c
	return &copy
}

func (s *CachedClientCredentialsStore) SaveCredentials(providerID string, c *clientCredentials) error {
	if err := s.store.SaveCredentials(providerID, c); err != nil {
		return err
	}
	s.mu.Lock()
	s.items[clientStoreKey(providerID)] = cloneClientCredentials(c)
	s.mu.Unlock()
	return nil
}

func (s *CachedClientCredentialsStore) LoadCredentials(providerID string) (*clientCredentials, bool, error) {
	key := clientStoreKey(providerID)
	s.mu.RLock()
	c, ok := s.items[key]
	s.mu.RUnlock()
	if ok {
		return cloneClientCredentials(c), true, nil
	}
	c, ok, err := s.store.LoadCredentials(providerID)
	if err != nil || !ok {
		return nil, false, err
	}
	s.mu.Lock()
	s.items[key] = cloneClientCredentials(c)
	s.mu.Unlock()
	return cloneClientCredentials(c), true, nil
}

func (s *CachedClientCredentialsStore) DeleteCredentials(providerID string) bool {
	deleted := s.store.DeleteCredentials(providerID)
	s.mu.Lock()
	delete(s.items, clientStoreKey(providerID))
	s.mu.Unlock()
	return deleted
}

func (s *CachedClientCredentialsStore) Clear() {
	s.mu.Lock()
	clear(s.items)
	s.mu.Unlock()
}
