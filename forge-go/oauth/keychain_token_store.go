package oauth

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// storedEntry is the JSON-serializable form of tokenEntry for keychain persistence.
type storedEntry struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	AuthURL      string    `json:"auth_url"`
	TokenURL     string    `json:"token_url"`
	AuthStyle    int       `json:"auth_style"`
	Scopes       []string  `json:"scopes"`
	// Resource is the RFC 8707 resource indicator, needed to refresh the token
	// with the same audience. Absent in entries written before it was supported.
	Resource string `json:"resource,omitempty"`
}

func toStoredEntry(e *tokenEntry) *storedEntry {
	return &storedEntry{
		AccessToken:  e.token.AccessToken,
		TokenType:    e.token.TokenType,
		RefreshToken: e.token.RefreshToken,
		Expiry:       e.token.Expiry,
		ClientID:     e.clientID,
		ClientSecret: e.clientSecret,
		AuthURL:      e.endpoint.AuthURL,
		TokenURL:     e.endpoint.TokenURL,
		AuthStyle:    int(e.endpoint.AuthStyle),
		Scopes:       e.scopes,
		Resource:     e.resource,
	}
}

func fromStoredEntry(s *storedEntry) *tokenEntry {
	return &tokenEntry{
		token: &oauth2.Token{
			AccessToken:  s.AccessToken,
			TokenType:    s.TokenType,
			RefreshToken: s.RefreshToken,
			Expiry:       s.Expiry,
		},
		clientID:     s.ClientID,
		clientSecret: s.ClientSecret,
		endpoint: oauth2.Endpoint{
			AuthURL:   s.AuthURL,
			TokenURL:  s.TokenURL,
			AuthStyle: oauth2.AuthStyle(s.AuthStyle),
		},
		scopes:   s.Scopes,
		resource: s.Resource,
	}
}

// KeychainTokenStore persists OAuth tokens in the OS keychain (macOS Keychain,
// Windows Credential Manager, Linux Secret Service via libsecret).
//
// Set FORGE_OAUTH_TOKEN_STORE=keychain to activate this backend.
type KeychainTokenStore struct {
	service string
}

func NewKeychainTokenStore() *KeychainTokenStore {
	return NewKeychainTokenStoreWithService(forgepath.KeychainService())
}

func NewKeychainTokenStoreWithService(service string) *KeychainTokenStore {
	return &KeychainTokenStore{service: service}
}

func (s *KeychainTokenStore) Save(orgID, providerID string, entry *tokenEntry) error {
	data, err := json.Marshal(toStoredEntry(entry))
	if err != nil {
		return fmt.Errorf("marshaling token: %w", err)
	}
	if err := keyring.Set(s.service, StoreKey(orgID, providerID), string(data)); err != nil {
		return fmt.Errorf("saving to keychain: %w", err)
	}
	return nil
}

func (s *KeychainTokenStore) Load(orgID, providerID string) (*tokenEntry, bool) {
	data, err := keyring.Get(s.service, StoreKey(orgID, providerID))
	if err != nil {
		return nil, false
	}
	var se storedEntry
	if err := json.Unmarshal([]byte(data), &se); err != nil {
		return nil, false
	}
	return fromStoredEntry(&se), true
}

func (s *KeychainTokenStore) Delete(orgID, providerID string) bool {
	return keyring.Delete(s.service, StoreKey(orgID, providerID)) == nil
}
