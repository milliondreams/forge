package secrets

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// CachedProvider keeps successfully resolved values in process memory and
// coalesces concurrent keychain reads. Missing values are deliberately not
// cached so newly configured credentials become visible without a restart.
type CachedProvider struct {
	provider SecretProvider
	mu       sync.RWMutex
	values   map[string]string
	group    singleflight.Group
}

func NewCachedProvider(provider SecretProvider) *CachedProvider {
	return &CachedProvider{provider: provider, values: make(map[string]string)}
}

func (p *CachedProvider) Resolve(ctx context.Context, key string) (string, error) {
	p.mu.RLock()
	value, ok := p.values[key]
	p.mu.RUnlock()
	if ok {
		return value, nil
	}

	resolved, err, _ := p.group.Do(key, func() (interface{}, error) {
		p.mu.RLock()
		value, ok := p.values[key]
		p.mu.RUnlock()
		if ok {
			return value, nil
		}
		value, err := p.provider.Resolve(ctx, key)
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		p.values[key] = value
		p.mu.Unlock()
		return value, nil
	})
	if err != nil {
		return "", err
	}
	return resolved.(string), nil
}

// ResolveMany resolves each unique key once and returns values keyed by the
// original secret name.
func (p *CachedProvider) ResolveMany(ctx context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		value, err := p.Resolve(ctx, key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

// ResolveBatch resolves every unique key and preserves per-key failures so a
// caller can apply fallback policy without repeating successful reads.
func (p *CachedProvider) ResolveBatch(ctx context.Context, keys []string) (map[string]string, map[string]error) {
	values := make(map[string]string, len(keys))
	errs := make(map[string]error)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		value, err := p.Resolve(ctx, key)
		if err != nil {
			errs[key] = err
			continue
		}
		values[key] = value
	}
	return values, errs
}

func (p *CachedProvider) Invalidate(key string) {
	p.mu.Lock()
	delete(p.values, key)
	p.mu.Unlock()
	p.group.Forget(key)
}

func (p *CachedProvider) Clear() {
	p.mu.Lock()
	clear(p.values)
	p.mu.Unlock()
}
