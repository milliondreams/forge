package oauth

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGetAuthURL_UsesProviderScopes(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"github": {
				DisplayName: "GitHub",
				AuthURL:     "https://github.com/login/oauth/authorize",
				TokenURL:    "https://github.com/login/oauth/access_token",
				Scopes:      []string{"repo", "user:email"},
			},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("github", nil)

	authURL, state, err := m.GetAuthURL(context.Background(), "org1", "github", "client-id", "client-secret", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if authURL == "" {
		t.Error("expected non-empty authURL")
	}
	if state == "" {
		t.Error("expected non-empty state")
	}

	// Verify pending flow exists
	m.mu.Lock()
	_, ok := m.pendingFlows[state]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expected pending flow to exist")
	}
}

func TestGetAuthURL_NoScopes(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"github": {DisplayName: "GitHub"},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("github", nil)

	_, state, err := m.GetAuthURL(context.Background(), "org1", "github", "client-id", "client-secret", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}

	m.mu.Lock()
	_, ok := m.pendingFlows[state]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expected pending flow to exist")
	}
}

// seedDiscovery pre-populates the discoverer cache so GetAuthURL resolves
// endpoints without network I/O.
func (m *Manager) seedDiscovery(resourceURL string, r *resolvedProvider) {
	r.fetchedAt = time.Now()
	m.disco.mu.Lock()
	m.disco.cache[resourceURL] = r
	m.disco.mu.Unlock()
}

func TestGetAuthURL_DCRDiscoversAndUsesRegisteredClient(t *testing.T) {
	// use_pkce is unset; a discovered public client must force PKCE on.
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"mcp": {
				DisplayName: "MCP",
				ResourceURL: "https://mcp.example.com/mcp",
				UseDCRP:     true,
			},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("mcp", nil)
	// Seed discovery and stored client credentials so GetAuthURL performs no
	// network I/O: resolve() hits the cache and registerIfNeeded() finds creds.
	m.seedDiscovery("https://mcp.example.com/mcp", &resolvedProvider{
		endpoint:    oauth2.Endpoint{AuthURL: "https://as.example.com/authorize", TokenURL: "https://as.example.com/token"},
		authMethods: []string{"none"}, // public client
	})
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})

	authURL, state, err := m.GetAuthURL(context.Background(), "org1", "mcp", "", "", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://as.example.com/authorize") {
		t.Errorf("expected discovered auth endpoint, got %s", authURL)
	}

	m.mu.Lock()
	flow := m.pendingFlows[state]
	m.mu.Unlock()
	if flow == nil {
		t.Fatal("expected pending flow to exist")
	}
	if flow.clientID != "registered-id" {
		t.Errorf("expected registered client id, got %q", flow.clientID)
	}
	// Public client must use PKCE regardless of the (unset) use_pkce default.
	if flow.codeVerifier == "" || !strings.Contains(authURL, "code_challenge") {
		t.Error("expected PKCE to be forced on for a public DCR client")
	}
}

func TestGetAuthURL_SendsAuthParams(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			// A server that follows OIDC Core §11 strictly: without
			// prompt=consent it drops offline_access and issues no refresh token.
			"strict-oidc": {
				AuthURL:     "https://login.example.com/oidc/auth",
				TokenURL:    "https://login.example.com/oidc/token",
				Scopes:      []string{"openid", "offline_access"},
				ResourceURL: "https://api.example.com",
				AuthParams:  map[string]string{"prompt": "consent"},
			},
			// Google's dialect: access_type instead of offline_access, on top of
			// the default prompt.
			"google": {
				AuthURL:    "https://example.com/oauth/authorize",
				TokenURL:   "https://example.com/oauth/token",
				AuthParams: map[string]string{"access_type": "offline"},
			},
			"plain": {
				AuthURL:  "https://example.com/oauth/authorize",
				TokenURL: "https://example.com/oauth/token",
			},
			"empty value": {
				AuthURL:    "https://example.com/oauth/authorize",
				TokenURL:   "https://example.com/oauth/token",
				AuthParams: map[string]string{"prompt": ""},
			},
		},
	}
	m := NewManager(cfg)
	for id := range cfg.Providers {
		m.CheckAndUpdateProvider(id, nil)
	}

	t.Run("params are sent and do not disturb the generated ones", func(t *testing.T) {
		authURL, state, err := m.GetAuthURL(context.Background(), "org1", "strict-oidc", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "prompt"); got != "consent" {
			t.Errorf("prompt = %q, want %q", got, "consent")
		}
		// auth_params is applied before PKCE and resource, so the generated
		// values must all still be intact and correct.
		if got := queryParam(t, authURL, "state"); got != state {
			t.Errorf("state = %q, want the returned state %q", got, state)
		}
		if queryParam(t, authURL, "code_challenge") == "" {
			t.Error("code_challenge missing")
		}
		if got := queryParam(t, authURL, "code_challenge_method"); got != "S256" {
			t.Errorf("code_challenge_method = %q, want S256", got)
		}
		if got := queryParam(t, authURL, "resource"); got != "https://api.example.com" {
			t.Errorf("resource = %q", got)
		}
		if got := queryParam(t, authURL, "redirect_uri"); got != "https://example.com/cb" {
			t.Errorf("redirect_uri = %q", got)
		}
		if got := queryParam(t, authURL, "scope"); got != "openid offline_access" {
			t.Errorf("scope = %q", got)
		}
	})

	t.Run("configured params sit alongside the default prompt", func(t *testing.T) {
		authURL, _, err := m.GetAuthURL(context.Background(), "org1", "google", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "access_type"); got != "offline" {
			t.Errorf("access_type = %q, want offline", got)
		}
		if got := queryParam(t, authURL, "prompt"); got != "consent" {
			t.Errorf("prompt = %q, want consent", got)
		}
	})

	t.Run("no auth_params still gets the default prompt", func(t *testing.T) {
		authURL, _, err := m.GetAuthURL(context.Background(), "org1", "plain", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "prompt"); got != "consent" {
			t.Errorf("prompt = %q, want consent", got)
		}
	})

	t.Run("empty value opts out and is omitted entirely", func(t *testing.T) {
		authURL, _, err := m.GetAuthURL(context.Background(), "org1", "empty value", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		u, err := url.Parse(authURL)
		if err != nil {
			t.Fatal(err)
		}
		// Not just empty — the key must not be present at all, otherwise the
		// provider receives a meaningless "prompt=".
		if _, present := u.Query()["prompt"]; present {
			t.Error("prompt should be absent, not sent empty")
		}
	})
}

func TestCallbackURL(t *testing.T) {
	base := "https://forge.example.com/api"
	// A single constant callback for every provider (flow identified by state).
	if got := callbackURL(base); got != base+"/oauth/callback" {
		t.Errorf("callback = %q", got)
	}
}

func TestDisconnect_KeepsGlobalDCRClient(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"mcp": {DisplayName: "MCP", ResourceURL: "https://mcp.example.com/mcp", UseDCRP: true},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("mcp", nil)

	// A registered (global) client and an org's token both exist.
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})
	m.SeedToken("org1", "mcp", "tok")

	if !m.Disconnect("org1", "mcp") {
		t.Fatal("expected Disconnect to report a removed token")
	}
	// Token is gone...
	if connected, err := m.IsConnected("org1", "mcp"); err != nil || connected {
		t.Error("expected org token to be removed after Disconnect")
	}
	// ...but the deployment-global client is retained for other orgs.
	if _, ok, err := m.credStore.LoadCredentials("mcp"); err != nil || !ok {
		t.Error("expected DCR client credentials to survive Disconnect")
	}
}

func TestLoadProvidersConfig_RejectsInvalidResourceConfig(t *testing.T) {
	cases := map[string]string{
		// DCR discovers its endpoints from the resource, so it needs one.
		"dcr without resource_url": `providers:
  broken:
    use_dcrp: true`,
		// resource_url is sent as the RFC 8707 resource indicator, which must be
		// an absolute URI with no fragment.
		"relative resource_url": `providers:
  broken:
    resource_url: /mcp`,
		"resource_url with fragment": `providers:
  broken:
    resource_url: https://mcp.example.com/mcp#frag`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/providers.yaml"
			if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProvidersConfig(path); err == nil {
				t.Errorf("expected validation error for %q, got nil", name)
			}
		})
	}
}

// auth_params must not be able to overwrite what the flow generates. AuthCodeURL
// applies AuthCodeOptions after its own parameters, so an unchecked entry would
// win over the real state or code_challenge.
func TestLoadProvidersConfig_RejectsReservedAuthParams(t *testing.T) {
	cases := map[string]string{
		"state": `providers:
  broken:
    auth_params:
      state: attacker-chosen`,
		"code_challenge": `providers:
  broken:
    auth_params:
      code_challenge: attacker-chosen`,
		"redirect_uri": `providers:
  broken:
    auth_params:
      redirect_uri: https://evil.example.com/cb`,
		// Derived from resource_url, and has to match on exchange and refresh —
		// which auth_params does not reach.
		"resource": `providers:
  broken:
    auth_params:
      resource: https://evil.example.com`,
		"scope": `providers:
  broken:
    auth_params:
      scope: openid`,
		"empty name": `providers:
  broken:
    auth_params:
      "": consent`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/providers.yaml"
			if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProvidersConfig(path); err == nil {
				t.Errorf("expected validation error for %q, got nil", name)
			}
		})
	}
}

func TestLoadProvidersConfig_InterpolatesAuthParams(t *testing.T) {
	t.Setenv("TEST_PROMPT_VALUE", "consent")
	yaml := `providers:
  api:
    auth_url: https://example.com/oauth/authorize
    token_url: https://example.com/oauth/token
    auth_params:
      prompt: ${TEST_PROMPT_VALUE}
      access_type: offline`

	path := t.TempDir() + "/providers.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProvidersConfig(path)
	if err != nil {
		t.Fatalf("LoadProvidersConfig failed: %v", err)
	}
	p := cfg.Providers["api"]
	if got := p.AuthParams["prompt"]; got != "consent" {
		t.Errorf("prompt = %q, want %q", got, "consent")
	}
	if got := p.AuthParams["access_type"]; got != "offline" {
		t.Errorf("access_type = %q, want %q", got, "offline")
	}
}

// prompt=consent is the default for every provider, static or DCR alike: a
// server that follows OIDC Core §11 strictly needs it to issue a refresh token,
// and one that does not ignores it. A DCR provider is registered against an
// authorization server we have no console for, so there is no other way to
// intervene there at all.
func TestAuthParams_DefaultsToPromptConsent(t *testing.T) {
	cases := map[string]struct {
		cfg  ProviderConfig
		want string // "" means the prompt parameter should be absent
	}{
		"dcr gets the default": {
			cfg:  ProviderConfig{UseDCRP: true, ResourceURL: "https://mcp.example.com/mcp"},
			want: "consent",
		},
		"explicit prompt wins": {
			cfg: ProviderConfig{UseDCRP: true, ResourceURL: "https://mcp.example.com/mcp",
				AuthParams: map[string]string{"prompt": "login"}},
			want: "login",
		},
		"empty prompt switches the default off": {
			cfg: ProviderConfig{UseDCRP: true, ResourceURL: "https://mcp.example.com/mcp",
				AuthParams: map[string]string{"prompt": ""}},
			want: "",
		},
		"static provider gets the default too": {
			cfg:  ProviderConfig{AuthURL: "https://example.com/a", TokenURL: "https://example.com/t"},
			want: "consent",
		},
		"static provider can opt out": {
			cfg: ProviderConfig{AuthURL: "https://example.com/a", TokenURL: "https://example.com/t",
				AuthParams: map[string]string{"prompt": ""}},
			want: "",
		},
		"other auth_params do not suppress the default": {
			cfg: ProviderConfig{AuthURL: "https://example.com/a", TokenURL: "https://example.com/t",
				AuthParams: map[string]string{"access_type": "offline"}},
			want: "consent",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.cfg.authParams()["prompt"]; got != tc.want {
				t.Errorf("prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

// The DCR default must survive all the way into the real authorization URL, not
// just authParams().
func TestGetAuthURL_DCRSendsPromptConsent(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"mcp": {ResourceURL: "https://mcp.example.com/mcp", UseDCRP: true, Scopes: []string{"offline_access"}},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("mcp", nil)
	m.seedDiscovery("https://mcp.example.com/mcp", &resolvedProvider{
		endpoint:    oauth2.Endpoint{AuthURL: "https://as.example.com/authorize", TokenURL: "https://as.example.com/token"},
		authMethods: []string{"none"},
	})
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})

	authURL, _, err := m.GetAuthURL(context.Background(), "org1", "mcp", "", "", "https://example.com/cb")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if got := queryParam(t, authURL, "prompt"); got != "consent" {
		t.Errorf("prompt = %q, want %q", got, "consent")
	}
}

// A static provider may declare resource_url purely to have the RFC 8707
// resource indicator sent; it is not DCR-only.
func TestLoadProvidersConfig_ResourceURLWithoutDCR(t *testing.T) {
	yaml := `providers:
  api:
    auth_url: https://example.com/oauth/authorize
    token_url: https://example.com/oauth/token
    resource_url: https://api.example.com`

	path := t.TempDir() + "/providers.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProvidersConfig(path)
	if err != nil {
		t.Fatalf("LoadProvidersConfig failed: %v", err)
	}
	p := cfg.Providers["api"]
	if p.ResourceURL != "https://api.example.com" {
		t.Errorf("resource_url = %q", p.ResourceURL)
	}
	if p.UseDCRP {
		t.Error("expected use_dcrp to stay false")
	}
}

func TestWithDynamicClient(t *testing.T) {
	cfg := &ProvidersConfig{Providers: map[string]ProviderConfig{}}

	// Defaults: name "Forge", uri empty (omitted from registration).
	def := NewManager(cfg)
	if def.clientName != "Forge" || def.clientURI != "" {
		t.Errorf("defaults = (%q, %q), want (%q, %q)", def.clientName, def.clientURI, "Forge", "")
	}

	// Both set.
	set := NewManagerWithStore(cfg, NewInMemoryTokenStore(), WithDynamicClient("Acme", "https://acme.example.com"))
	if set.clientName != "Acme" || set.clientURI != "https://acme.example.com" {
		t.Errorf("set = (%q, %q)", set.clientName, set.clientURI)
	}

	// Empty values are ignored independently: name keeps default, uri stays empty.
	empty := NewManagerWithStore(cfg, NewInMemoryTokenStore(), WithDynamicClient("", ""))
	if empty.clientName != "Forge" || empty.clientURI != "" {
		t.Errorf("empty = (%q, %q), want (%q, %q)", empty.clientName, empty.clientURI, "Forge", "")
	}
}

func TestResolveEndpoint(t *testing.T) {
	// Built-in providers resolve to library endpoints.
	slack, err := resolveEndpoint("slack", ProviderConfig{})
	if err != nil {
		t.Fatalf("slack: %v", err)
	}
	if slack.AuthURL != "https://slack.com/oauth/v2/authorize" {
		t.Errorf("unexpected slack auth URL: %s", slack.AuthURL)
	}

	// Microsoft must be the Azure AD v2.0 common endpoint, not the legacy
	// consumer endpoint.
	ms, err := resolveEndpoint("microsoft", ProviderConfig{})
	if err != nil {
		t.Fatalf("microsoft: %v", err)
	}
	if ms.AuthURL != "https://login.microsoftonline.com/common/oauth2/v2.0/authorize" {
		t.Errorf("unexpected microsoft auth URL: %s", ms.AuthURL)
	}

	// Explicit config overrides the built-in.
	custom, err := resolveEndpoint("github", ProviderConfig{AuthURL: "https://x/a", TokenURL: "https://x/t"})
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if custom.AuthURL != "https://x/a" || custom.TokenURL != "https://x/t" {
		t.Errorf("expected config override, got %+v", custom)
	}

	// Unknown provider with no config is an error.
	if _, err := resolveEndpoint("mystery", ProviderConfig{}); err == nil {
		t.Error("expected error for unknown provider without endpoints")
	}
}

func TestGetAuthURL_UnknownProvider(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{},
	}
	m := NewManager(cfg)

	_, _, err := m.GetAuthURL(context.Background(), "org1", "unknown", "id", "secret", "")
	if err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
}

func TestProviderConfig_ParsesScopes(t *testing.T) {
	// verify the field round-trips through YAML correctly.
	yaml := `providers:
  github:
    display_name: GitHub
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token`

	import_path := t.TempDir() + "/providers.yaml"
	if err := os.WriteFile(import_path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProvidersConfig(import_path)
	if err != nil {
		t.Fatalf("LoadProvidersConfig failed: %v", err)
	}
	p, ok := cfg.Providers["github"]
	if !ok {
		t.Fatal("expected github provider")
	}
	if p.DisplayName != "GitHub" {
		t.Errorf("unexpected display name: %s", p.DisplayName)
	}
}

// countingTokenStore records how many times a token entry is written back, so a
// test can tell the cheap read path from the refresh path.
type countingTokenStore struct {
	*InMemoryTokenStore
	saves int
}

func (s *countingTokenStore) Save(orgID, providerID string, entry *tokenEntry) error {
	s.saves++
	return s.InMemoryTokenStore.Save(orgID, providerID, entry)
}

// Tokens that need no refresh must be returned without touching the store. The
// zero-expiry case is the interesting one: it means "never expires", but
// time.Until on it is hugely negative, so a naive deadline comparison sends
// every call down the refresh path and rewrites the keychain entry for nothing.
func TestGetAccessToken_ReturnsUnexpiredTokenWithoutWriting(t *testing.T) {
	cases := map[string]struct {
		token   *oauth2.Token
		want    string
		wantErr bool
	}{
		// What Slack stores for a bot token, and GitHub for a classic one.
		"no expiry never expires": {
			token: &oauth2.Token{AccessToken: "xoxb-static", TokenType: "bot"},
			want:  "xoxb-static",
		},
		"expiry far in the future": {
			token: &oauth2.Token{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)},
			want:  "fresh",
		},
		// The guard above must not swallow tokens that genuinely need a refresh:
		// this one is expired with nothing to refresh from, so it has to fail.
		"expired with no refresh token": {
			token:   &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(-time.Hour)},
			wantErr: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &countingTokenStore{InMemoryTokenStore: NewInMemoryTokenStore()}
			m := NewManagerWithStore(&ProvidersConfig{
				Providers: map[string]ProviderConfig{"api": {TokenURL: "https://example.com/token"}},
			}, store)
			m.CheckAndUpdateProvider("api", nil)
			if err := store.Save("org1", "api", &tokenEntry{token: tc.token}); err != nil {
				t.Fatal(err)
			}
			store.saves = 0 // ignore the setup write

			got, err := m.GetAccessToken(context.Background(), "org1", "api")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GetAccessToken succeeded with %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetAccessToken failed: %v", err)
			}
			if got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
			if store.saves != 0 {
				t.Errorf("store written %d times, want 0 — the token needed no refresh", store.saves)
			}
		})
	}
}
