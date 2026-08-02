package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/credentials"
	"golang.org/x/oauth2"
)

func newCredentialVaultForTest(t *testing.T) *credentials.Vault {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vault.Close)
	return vault
}

func TestCredentialTokenStoreRoundTrip(t *testing.T) {
	vault := newCredentialVaultForTest(t)
	store := NewCredentialTokenStore(vault)
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	want := &tokenEntry{
		token:        &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", Expiry: expiresAt},
		clientID:     "client-id",
		clientSecret: "client-secret",
		scopes:       []string{"read", "write"},
		resource:     "https://api.example.com",
	}
	if err := store.Save("org", "provider", want); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Load("org", "provider")
	if !ok || got.token.AccessToken != want.token.AccessToken || got.token.RefreshToken != want.token.RefreshToken ||
		got.clientID != want.clientID || got.clientSecret != want.clientSecret || got.resource != want.resource ||
		!got.token.Expiry.Equal(expiresAt) {
		t.Fatalf("unexpected token round trip: %#v, ok=%v", got, ok)
	}
	if deleted, err := store.Delete("org", "provider"); err != nil || !deleted {
		t.Fatalf("Delete() = %v, %v", deleted, err)
	}
}

func TestCredentialClientCredentialsStoreRoundTrip(t *testing.T) {
	vault := newCredentialVaultForTest(t)
	store := NewCredentialClientCredentialsStore(vault)
	want := &clientCredentials{ClientID: "client-id", ClientSecret: "client-secret"}
	if err := store.SaveCredentials("provider", want); err != nil {
		t.Fatal(err)
	}
	got, ok := store.LoadCredentials("provider")
	if !ok || got.ClientID != want.ClientID || got.ClientSecret != want.ClientSecret {
		t.Fatalf("unexpected client credentials round trip: %#v, ok=%v", got, ok)
	}
}
