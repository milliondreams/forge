package secrets

import (
	"errors"
	"sync"
)

// ErrSecretExists is returned by Manager.Set when a secret with the same name
// already exists for the org.
var ErrSecretExists = errors.New("secret already exists")

// Manager provides org-scoped secret CRUD over a value store and a separate
// value-free metadata index. Its mutex keeps keychain and metadata mutations
// ordered against concurrent callers.
//
// The Manager deliberately exposes no method that returns a secret value:
// values are read only by a SecretProvider during resolution, never through the
// management API, so they cannot be leaked via an HTTP handler.
type Manager struct {
	mu          sync.Mutex
	store       SecretStore
	metadata    MetadataIndex
	invalidator interface{ Invalidate(string) }
}

// NewManager constructs a Manager backed by the given SecretStore.
func NewManager(store SecretStore) *Manager {
	return &Manager{store: store, metadata: storeMetadataAdapter{store: store}}
}

func NewManagerWithInvalidator(store SecretStore, invalidator interface{ Invalidate(string) }) *Manager {
	return NewManagerWithMetadata(store, storeMetadataAdapter{store: store}, invalidator)
}

func NewManagerWithMetadata(store SecretStore, metadata MetadataIndex, invalidator interface{ Invalidate(string) }) *Manager {
	return &Manager{store: store, metadata: metadata, invalidator: invalidator}
}

type storeMetadataAdapter struct{ store SecretStore }

func (a storeMetadataAdapter) Add(string, string) error            { return nil }
func (a storeMetadataAdapter) Remove(orgID, name string) bool      { return a.store.Delete(orgID, name) }
func (a storeMetadataAdapter) Exists(orgID, name string) bool      { return a.store.Exists(orgID, name) }
func (a storeMetadataAdapter) List(orgID string) ([]string, error) { return a.store.List(orgID) }

func (m *Manager) invalidate(orgID, name string) {
	if m.invalidator != nil {
		m.invalidator.Invalidate(SecretStoreKey(orgID, name))
	}
}

// Set creates a new secret for the org. It returns ErrSecretExists if a secret
// with the same name already exists (use Update to change an existing value).
func (m *Manager) Set(orgID, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.metadata.Exists(orgID, name) {
		return ErrSecretExists
	}
	if err := m.store.Save(orgID, name, value); err != nil {
		return err
	}
	if err := m.metadata.Add(orgID, name); err != nil {
		_ = m.store.Delete(orgID, name)
		return err
	}
	m.invalidate(orgID, name)
	return nil
}

// Update replaces the value of an existing secret. It returns ErrSecretNotFound
// if no secret with that name exists for the org.
func (m *Manager) Update(orgID, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.metadata.Exists(orgID, name) {
		return ErrSecretNotFound
	}
	if err := m.store.Save(orgID, name, value); err != nil {
		return err
	}
	m.invalidate(orgID, name)
	return nil
}

// Delete removes a secret. It returns false if no secret with that name exists
// for the org.
func (m *Manager) Delete(orgID, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := m.store.Delete(orgID, name)
	if deleted {
		m.metadata.Remove(orgID, name)
		m.invalidate(orgID, name)
	}
	return deleted
}

// List returns the names of the org's secrets, sorted. It never returns values.
func (m *Manager) List(orgID string) ([]string, error) {
	return m.metadata.List(orgID)
}

// Exists reports whether a secret with that name exists for the org.
func (m *Manager) Exists(orgID, name string) bool {
	return m.metadata.Exists(orgID, name)
}
