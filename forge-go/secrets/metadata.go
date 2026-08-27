package secrets

import (
	"sort"
	"sync"
)

// MetadataIndex tracks secret names without storing their values. Implementations
// may persist this data in the Forge metastore.
type MetadataIndex interface {
	Add(orgID, name string) error
	Remove(orgID, name string) bool
	Exists(orgID, name string) bool
	List(orgID string) ([]string, error)
}

type InMemoryMetadataIndex struct {
	mu    sync.RWMutex
	items map[string]struct{}
}

func NewInMemoryMetadataIndex() *InMemoryMetadataIndex {
	return &InMemoryMetadataIndex{items: make(map[string]struct{})}
}

func (i *InMemoryMetadataIndex) Add(orgID, name string) error {
	i.mu.Lock()
	i.items[SecretStoreKey(orgID, name)] = struct{}{}
	i.mu.Unlock()
	return nil
}

func (i *InMemoryMetadataIndex) Remove(orgID, name string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := SecretStoreKey(orgID, name)
	_, exists := i.items[key]
	delete(i.items, key)
	return exists
}

func (i *InMemoryMetadataIndex) Exists(orgID, name string) bool {
	i.mu.RLock()
	_, exists := i.items[SecretStoreKey(orgID, name)]
	i.mu.RUnlock()
	return exists
}

func (i *InMemoryMetadataIndex) List(orgID string) ([]string, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	names := make([]string, 0)
	for key := range i.items {
		if itemOrg, name, ok := ParseSecretKey(key); ok && itemOrg == orgID {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
