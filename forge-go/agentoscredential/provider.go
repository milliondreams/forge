package agentoscredential

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/rustic-ai/forge/forge-go/credentials"
	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

type Provider struct {
	store credentials.Store
	oauth *oauth.Manager
}

func NewProvider(store credentials.Store, oauthManager *oauth.Manager) *Provider {
	return &Provider{store: store, oauth: oauthManager}
}

func (p *Provider) Resolve(ctx context.Context, key string) (string, error) {
	if orgID, providerID, ok := oauth.ParseOAuthKey(key); ok && p.oauth != nil {
		value, err := p.oauth.GetAccessToken(ctx, orgID, providerID)
		if errors.Is(err, oauth.ErrNotConnected) {
			return "", secrets.ErrSecretNotFound
		}
		return value, err
	}
	raw, ok := p.store.Get(key)
	if !ok {
		return "", secrets.ErrSecretNotFound
	}
	if _, _, isOAuth := oauth.ParseOAuthKey(key); isOAuth {
		var entry struct {
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal([]byte(raw), &entry) != nil || entry.AccessToken == "" {
			return "", secrets.ErrSecretNotFound
		}
		return entry.AccessToken, nil
	}
	return raw, nil
}
