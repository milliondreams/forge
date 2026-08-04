package oauth

import (
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

// ProviderConfig defines a named OAuth2 provider. Scopes and endpoint URLs are
// set here; credentials are supplied per-request by the caller.
type ProviderConfig struct {
	DisplayName string   `yaml:"display_name" json:"displayName,omitempty"`
	Description string   `yaml:"description" json:"description,omitempty"`
	AuthURL     string   `yaml:"auth_url" json:"authUrl,omitempty"`
	TokenURL    string   `yaml:"token_url" json:"tokenUrl,omitempty"`
	Scopes      []string `yaml:"scopes" json:"scopes,omitempty"`
	RedirectURL string   `yaml:"redirect_url" json:"redirectUrl,omitempty"`
	// UsePKCE controls whether PKCE (S256) is used. Defaults to true.
	// Set to false for providers that do not support it.
	UsePKCE *bool `yaml:"use_pkce" json:"usePkce,omitempty"`

	// AuthParams are extra parameters appended to the authorization request —
	// and only that request, never the token exchange or the refresh. They exist
	// for provider-specific knobs the generic flow does not model, because
	// providers disagree on how to ask for a refresh token:
	//
	//	prompt: consent        // authorization servers that follow OIDC Core §11
	//	                       // strictly silently drop the offline_access scope
	//	                       // without it, so no refresh token is ever issued.
	//	                       // Sent by default; see authParams.
	//	access_type: offline   // Google's proprietary offline_access equivalent
	//
	// A parameter whose value is empty is left out entirely; that is how a
	// built-in default is switched off (see authParams).
	//
	// Parameters the flow generates for itself — state, code_challenge, and the
	// rest of reservedAuthParams — cannot be set here; they are rejected at
	// load time.
	AuthParams map[string]string `yaml:"auth_params" json:"authParams,omitempty"`

	// ResourceURL is the canonical URI of the OAuth2 protected resource this
	// provider fronts (e.g. an MCP server endpoint).
	//
	// Whenever it is set — with or without UseDCRP — it is sent as the RFC 8707
	// `resource` parameter on the authorization, token-exchange and refresh
	// requests, asking the authorization server for a token audience-restricted
	// to that resource. MCP servers require this; static providers whose
	// authorization server does not understand the parameter should leave
	// resource_url unset.
	//
	// With UseDCRP it is additionally the discovery input: the
	// auth/token/registration endpoints are read from the metadata it advertises
	// (RFC 9728 + RFC 8414). Static providers still need AuthURL and TokenURL
	// (or a built-in endpoint) — resource_url alone discovers nothing for them.
	ResourceURL string `yaml:"resource_url" json:"resourceUrl,omitempty"`
	// UseDCRP enables Dynamic Client Registration (RFC 7591): the endpoints are
	// discovered from ResourceURL and the client_id/client_secret are registered
	// with the provider on demand instead of being supplied by the caller.
	// Requires ResourceURL. Defaults to false.
	UseDCRP bool `yaml:"use_dcrp" json:"useDcrp,omitempty"`
}

// RequiresClientCredentials reports whether the caller must supply a client_id
// and client_secret when starting an auth flow. Providers using Dynamic Client
// Registration register their own credentials and require none.
func (p ProviderConfig) RequiresClientCredentials() bool {
	return !p.UseDCRP
}

// reservedAuthParams are the authorization-request parameters the flow derives
// itself, so auth_params is not allowed to supply them.
//
// The restriction is load-bearing rather than tidiness:
// oauth2.Config.AuthCodeURL writes its own parameters first and then applies
// each AuthCodeOption with url.Values.Set, so a colliding auth_params entry
// would silently overwrite the generated value — including state, which is the
// CSRF defence, and code_challenge, which is what binds the code to this
// client. resource is reserved too: it is derived from ResourceURL and has to
// match on the exchange and refresh, which auth_params does not reach.
var reservedAuthParams = map[string]struct{}{
	"client_id":             {},
	"client_secret":         {},
	"code":                  {},
	"code_challenge":        {},
	"code_challenge_method": {},
	"code_verifier":         {},
	"grant_type":            {},
	"redirect_uri":          {},
	"resource":              {},
	"response_type":         {},
	"scope":                 {},
	"state":                 {},
}

// authParams returns the parameters to append to the authorization request.
//
// Every provider gets prompt=consent by default, so the behaviour is the same
// whether the far side is a static provider or one registered by DCR. It is the
// safe direction to be wrong in: a server that follows OIDC Core §11 strictly
// needs it to issue a refresh token at all, and one that does not simply
// ignores it. An explicit prompt in auth_params takes precedence; set it to the
// empty string to opt out, for a provider that rejects the parameter.
//
// The default only has an effect together with offline_access in Scopes, which
// stays explicit per provider: an authorization server may reject an unknown
// scope outright (RFC 6749 §3.3), and turning "connects but cannot refresh" into
// "cannot connect" would be the worse failure.
func (p ProviderConfig) authParams() map[string]string {
	if _, set := p.AuthParams["prompt"]; set {
		return p.AuthParams
	}
	out := make(map[string]string, len(p.AuthParams)+1)
	maps.Copy(out, p.AuthParams)
	out["prompt"] = "consent"
	return out
}

// Validate reports configuration errors that would otherwise surface only when
// an auth flow is started: DCR discovers its endpoints from the resource, so it
// requires resource_url, and resource_url — which is sent as the RFC 8707
// `resource` parameter — must be a valid resource indicator: an absolute URI
// with no fragment (RFC 8707, section 2). auth_params must not collide with the
// parameters the flow generates for itself.
func (p ProviderConfig) Validate(id string) error {
	if p.UseDCRP && p.ResourceURL == "" {
		return fmt.Errorf("provider %q: use_dcrp requires resource_url", id)
	}
	// Sorted so a config with several bad names always reports the same one.
	for _, k := range slices.Sorted(maps.Keys(p.AuthParams)) {
		if k == "" {
			return fmt.Errorf("provider %q: auth_params has an empty parameter name", id)
		}
		if _, reserved := reservedAuthParams[k]; reserved {
			return fmt.Errorf("provider %q: auth_params must not set %q; the OAuth2 flow generates it", id, k)
		}
	}
	if p.ResourceURL != "" {
		u, err := url.Parse(p.ResourceURL)
		if err != nil {
			return fmt.Errorf("provider %q: invalid resource_url %q: %w", id, p.ResourceURL, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("provider %q: resource_url must be an absolute URI, got %q", id, p.ResourceURL)
		}
		if u.Fragment != "" {
			return fmt.Errorf("provider %q: resource_url must not contain a fragment, got %q", id, p.ResourceURL)
		}
	}
	return nil
}

// pkce returns whether PKCE should be used for this provider (default: true).
func (p ProviderConfig) pkce() bool {
	return p.UsePKCE == nil || *p.UsePKCE
}

// ProvidersConfig is the top-level structure of the oauth-providers.yaml file.
type ProvidersConfig struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
}

var envPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func interpolateEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
}

// LoadProvidersConfig reads and parses the YAML file at path. If the file does
// not exist, an empty config is returned without error.
func LoadProvidersConfig(path string) (*ProvidersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProvidersConfig{Providers: map[string]ProviderConfig{}}, nil
		}
		return nil, fmt.Errorf("reading oauth providers config: %w", err)
	}

	var cfg ProvidersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing oauth providers config: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}

	interpolated := make(map[string]ProviderConfig, len(cfg.Providers))
	for id, p := range cfg.Providers {
		p.AuthURL = interpolateEnv(p.AuthURL)
		p.TokenURL = interpolateEnv(p.TokenURL)
		p.RedirectURL = interpolateEnv(p.RedirectURL)
		p.ResourceURL = interpolateEnv(p.ResourceURL)
		if len(p.AuthParams) > 0 {
			// A fresh map rather than an in-place rewrite, so the parsed config
			// is not aliased by the interpolated one.
			params := make(map[string]string, len(p.AuthParams))
			for k, v := range p.AuthParams {
				params[k] = interpolateEnv(v)
			}
			p.AuthParams = params
		}
		if err := p.Validate(id); err != nil {
			return nil, fmt.Errorf("parsing oauth providers config: %w", err)
		}
		interpolated[id] = p
	}
	cfg.Providers = interpolated

	return &cfg, nil
}
