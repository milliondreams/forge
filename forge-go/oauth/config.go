package oauth

import (
	"fmt"
	"net/url"
	"os"
	"regexp"

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

// Validate reports configuration errors that would otherwise surface only when
// an auth flow is started: DCR discovers its endpoints from the resource, so it
// requires resource_url, and resource_url — which is sent as the RFC 8707
// `resource` parameter — must be a valid resource indicator: an absolute URI
// with no fragment (RFC 8707, section 2).
func (p ProviderConfig) Validate(id string) error {
	if p.UseDCRP && p.ResourceURL == "" {
		return fmt.Errorf("provider %q: use_dcrp requires resource_url", id)
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
		if err := p.Validate(id); err != nil {
			return nil, fmt.Errorf("parsing oauth providers config: %w", err)
		}
		interpolated[id] = p
	}
	cfg.Providers = interpolated

	return &cfg, nil
}
