package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// tokenServer serves a fixed token response and exposes the form of the last
// token request it received.
func tokenServer(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing token request: %v", err)
		}
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		// refresh_token is deliberately omitted: the caller must keep the old one.
		_, _ = w.Write([]byte(`{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestGetAuthURL_SendsResourceParam(t *testing.T) {
	const resource = "https://api.example.com"
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			// A static provider: resource_url is only there for RFC 8707.
			"api": {
				AuthURL:     "https://example.com/oauth/authorize",
				TokenURL:    "https://example.com/oauth/token",
				ResourceURL: resource,
			},
			"plain": {
				AuthURL:  "https://example.com/oauth/authorize",
				TokenURL: "https://example.com/oauth/token",
			},
			"mcp": {ResourceURL: "https://mcp.example.com/mcp", UseDCRP: true},
		},
	}
	m := NewManager(cfg)
	for _, id := range []string{"api", "plain", "mcp"} {
		m.CheckAndUpdateProvider(id, nil)
	}
	m.seedDiscovery("https://mcp.example.com/mcp", &resolvedProvider{
		endpoint:    oauth2.Endpoint{AuthURL: "https://as.example.com/authorize", TokenURL: "https://as.example.com/token"},
		authMethods: []string{"none"},
	})
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})

	t.Run("static provider with resource_url", func(t *testing.T) {
		authURL, state, err := m.GetAuthURL(context.Background(), "org1", "api", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "resource"); got != resource {
			t.Errorf("resource = %q, want %q", got, resource)
		}
		// The exchange has to repeat it, so the flow must carry it.
		m.mu.Lock()
		flow := m.pendingFlows[state]
		m.mu.Unlock()
		if flow == nil || flow.resource != resource {
			t.Errorf("pending flow resource = %q, want %q", flow.resource, resource)
		}
	})

	t.Run("no resource_url sends nothing", func(t *testing.T) {
		authURL, _, err := m.GetAuthURL(context.Background(), "org1", "plain", "cid", "csecret", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "resource"); got != "" {
			t.Errorf("resource = %q, want it absent", got)
		}
	})

	t.Run("dcr provider", func(t *testing.T) {
		authURL, _, err := m.GetAuthURL(context.Background(), "org1", "mcp", "", "", "https://example.com/cb")
		if err != nil {
			t.Fatalf("GetAuthURL failed: %v", err)
		}
		if got := queryParam(t, authURL, "resource"); got != "https://mcp.example.com/mcp" {
			t.Errorf("resource = %q, want the discovery resource_url", got)
		}
	})
}

func queryParam(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	return u.Query().Get(key)
}

func TestExchangeCode_SendsResourceParam(t *testing.T) {
	srv, form := tokenServer(t)
	const resource = "https://api.example.com"
	m := NewManager(&ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"api": {
				AuthURL:     srv.URL + "/authorize",
				TokenURL:    srv.URL + "/token",
				ResourceURL: resource,
			},
		},
	})
	m.CheckAndUpdateProvider("api", nil)

	ctx := context.Background()
	_, state, err := m.GetAuthURL(ctx, "org1", "api", "cid", "csecret", "https://example.com/cb")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if _, err := m.ExchangeCode(ctx, "the-code", state); err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	if got := form.Get("resource"); got != resource {
		t.Errorf("token request resource = %q, want %q", got, resource)
	}
	// Stored so the refresh can repeat it.
	entry, ok := m.store.Load("org1", "api")
	if !ok {
		t.Fatal("expected a stored token entry")
	}
	if entry.resource != resource {
		t.Errorf("stored resource = %q, want %q", entry.resource, resource)
	}
}

func TestGetAccessToken_RefreshSendsResourceParam(t *testing.T) {
	const resource = "https://api.example.com"

	newManager := func(t *testing.T, tokenURL, entryResource string) *Manager {
		m := NewManager(&ProvidersConfig{
			Providers: map[string]ProviderConfig{"api": {TokenURL: tokenURL, ResourceURL: resource}},
		})
		m.CheckAndUpdateProvider("api", nil)
		// An expired token with a refresh token: GetAccessToken must refresh it.
		if err := m.store.Save("org1", "api", &tokenEntry{
			token: &oauth2.Token{
				AccessToken:  "old-access",
				TokenType:    "Bearer",
				RefreshToken: "the-refresh-token",
				Expiry:       time.Now().Add(-time.Minute),
			},
			clientID:     "cid",
			clientSecret: "csecret",
			endpoint:     oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
			resource:     entryResource,
		}); err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("resource repeated on refresh", func(t *testing.T) {
		srv, form := tokenServer(t)
		m := newManager(t, srv.URL+"/token", resource)

		tok, err := m.GetAccessToken(context.Background(), "org1", "api")
		if err != nil {
			t.Fatalf("GetAccessToken failed: %v", err)
		}
		if tok != "new-access" {
			t.Errorf("token = %q, want the refreshed one", tok)
		}
		if got := form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", got)
		}
		if got := form.Get("resource"); got != resource {
			t.Errorf("refresh resource = %q, want %q", got, resource)
		}
		if got := form.Get("refresh_token"); got != "the-refresh-token" {
			t.Errorf("refresh_token = %q, want it forwarded intact", got)
		}
		// The response omitted refresh_token, so the old one must survive.
		entry, _ := m.store.Load("org1", "api")
		if entry.token.RefreshToken != "the-refresh-token" {
			t.Errorf("stored refresh token = %q, want it preserved", entry.token.RefreshToken)
		}
		if entry.resource != resource {
			t.Errorf("stored resource = %q, want it preserved", entry.resource)
		}
	})

	t.Run("no resource sends nothing", func(t *testing.T) {
		srv, form := tokenServer(t)
		m := newManager(t, srv.URL+"/token", "")

		if _, err := m.GetAccessToken(context.Background(), "org1", "api"); err != nil {
			t.Fatalf("GetAccessToken failed: %v", err)
		}
		if got := form.Get("resource"); got != "" {
			t.Errorf("resource = %q, want it absent", got)
		}
	})
}

// recordingRT captures the request its wrapper hands down.
type recordingRT struct {
	req  *http.Request
	body string
}

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.req = req
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		r.body = string(b)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func TestResourceParamTransport(t *testing.T) {
	const resource = "https://api.example.com"

	roundTrip := func(t *testing.T, req *http.Request) *recordingRT {
		t.Helper()
		base := &recordingRT{}
		rt := &resourceParamTransport{base: base, resource: resource}
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip failed: %v", err)
		}
		return base
	}

	t.Run("adds resource to the form body", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://as.example.com/token",
			strings.NewReader("grant_type=refresh_token&refresh_token=rt"))
		if err != nil {
			t.Fatal(err)
		}
		got := roundTrip(t, req)

		vals, err := url.ParseQuery(got.body)
		if err != nil {
			t.Fatalf("parsing forwarded body %q: %v", got.body, err)
		}
		if vals.Get("resource") != resource {
			t.Errorf("resource = %q, want %q", vals.Get("resource"), resource)
		}
		if vals.Get("refresh_token") != "rt" {
			t.Errorf("existing params lost: %q", got.body)
		}
		if got.req.ContentLength != int64(len(got.body)) {
			t.Errorf("ContentLength = %d, want %d", got.req.ContentLength, len(got.body))
		}
		if got.req == req {
			t.Error("expected the caller's request to be left alone")
		}
	})

	t.Run("passes a bodiless request through", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "https://as.example.com/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := roundTrip(t, req); got.body != "" || got.req.URL.Query().Get("resource") != "" {
			t.Errorf("request was modified: body %q, url %s", got.body, got.req.URL)
		}
	})
}

func TestResourceParamClient_KeepsManagerTimeout(t *testing.T) {
	m := NewManager(&ProvidersConfig{Providers: map[string]ProviderConfig{}})
	c := m.resourceParamClient("https://api.example.com")
	if c.Timeout != m.httpClient.Timeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, m.httpClient.Timeout)
	}
	if _, ok := c.Transport.(*resourceParamTransport); !ok {
		t.Errorf("transport = %T, want *resourceParamTransport", c.Transport)
	}
}
