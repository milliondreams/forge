package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	extProvidersMu sync.RWMutex
	extProviders   = map[string]func() SecretProvider{}
)

// RegisterProvider registers an external provider type by name. Call from an
// init function in packages that cannot be imported here without a cycle.
func RegisterProvider(name string, fn func() SecretProvider) {
	extProvidersMu.Lock()
	defer extProvidersMu.Unlock()
	extProviders[name] = fn
}

func newProvider(name string) (SecretProvider, error) {
	switch name {
	case "env":
		return NewEnvSecretProvider(), nil
	case "dotenv":
		return NewDotEnvSecretProvider(""), nil
	case "file":
		return NewFileSecretProvider(""), nil
	default:
		extProvidersMu.RLock()
		fn, ok := extProviders[name]
		extProvidersMu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown secret provider type %q", name)
		}
		return fn(), nil
	}
}

type ChainSecretProvider struct {
	providers []SecretProvider
}

func NewChainSecretProvider(providers ...SecretProvider) *ChainSecretProvider {
	return &ChainSecretProvider{providers: providers}
}

func (p *ChainSecretProvider) Resolve(ctx context.Context, key string) (string, error) {
	for _, provider := range p.providers {
		val, err := provider.Resolve(ctx, key)
		if err == nil {
			return val, nil
		}
		if !errors.Is(err, ErrSecretNotFound) {
			return "", err
		}
	}
	return "", ErrSecretNotFound
}

// ParseProviderChain validates a comma-separated provider chain, removes
// duplicates, and preserves first-seen precedence. An empty value means the
// secure keychain-only default.
func ParseProviderChain(spec string) ([]string, bool, error) {
	if strings.TrimSpace(spec) == "" {
		spec = "keychain"
	}
	seen := make(map[string]struct{})
	names := make([]string, 0)
	unsafe := false
	for _, raw := range strings.Split(spec, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			return nil, false, fmt.Errorf("secret provider chain contains an empty provider")
		}
		switch name {
		case "keychain":
		case "env", "dotenv", "file":
			unsafe = true
		default:
			return nil, false, fmt.Errorf("unknown secret provider type %q", name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, false, fmt.Errorf("secret provider chain must not be empty")
	}
	return names, unsafe, nil
}

// NewProviderChain constructs a strict provider chain. It never consults the
// process environment for provider selection and never falls back silently.
func NewProviderChain(spec string) (*CachedProvider, []string, bool, error) {
	names, unsafe, err := ParseProviderChain(spec)
	if err != nil {
		return nil, nil, false, err
	}
	var providers []SecretProvider
	for _, name := range names {
		p, err := newProvider(name)
		if err != nil {
			return nil, nil, false, err
		}
		providers = append(providers, p)
	}
	return NewCachedProvider(NewChainSecretProvider(providers...)), names, unsafe, nil
}
