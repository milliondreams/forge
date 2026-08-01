//go:build !windows

package supervisor

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAgentNetworkRelayDestinationPolicy(t *testing.T) {
	relay := &AgentNetworkRelay{allowed: []string{
		"api.openai.com",
		"*.example.com",
		"10.0.2.101:55262",
	}}

	for _, test := range []struct {
		host    string
		port    string
		allowed bool
	}{
		{"api.openai.com", "443", true},
		{"models.example.com", "443", true},
		{"example.com", "443", false},
		{"10.0.2.101", "55262", true},
		{"10.0.2.101", "3001", false},
		{"127.0.0.1", "3001", false},
		{"169.254.169.254", "80", false},
	} {
		if got := relay.destinationAllowed(test.host, test.port); got != test.allowed {
			t.Errorf("destinationAllowed(%s,%s) = %v, want %v", test.host, test.port, got, test.allowed)
		}
	}
}

func TestAgentNetworkRelaysKeepPerAgentPoliciesSeparate(t *testing.T) {
	first := &AgentNetworkRelay{allowed: []string{"api.openai.com"}}
	second := &AgentNetworkRelay{allowed: []string{"api.anthropic.com"}}

	if !first.destinationAllowed("api.openai.com", "443") {
		t.Fatal("first agent should allow its configured destination")
	}
	if second.destinationAllowed("api.openai.com", "443") {
		t.Fatal("second agent inherited another agent's destination")
	}
}

func TestAgentNetworkRelayAllowsOnlyExplicitLoopbackControlEndpoint(t *testing.T) {
	relay := &AgentNetworkRelay{allowed: []string{"127.0.0.1:3001"}}

	if !relay.destinationAllowed("127.0.0.1", "3001") {
		t.Fatal("configured Forge loopback endpoint should be allowed")
	}
	for _, target := range [][2]string{{"127.0.0.1", "6379"}, {"::1", "3001"}, {"10.0.2.2", "3001"}} {
		if relay.destinationAllowed(target[0], target[1]) {
			t.Fatalf("unconfigured control destination %s:%s was allowed", target[0], target[1])
		}
	}
}

func TestAgentOSManagerDestinationRequiresLoopbackHTTPOrigin(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{"http://127.0.0.1:3001", "127.0.0.1:3001"},
		{"http://localhost:3001/", "localhost:3001"},
		{"http://[::1]:3001", "[::1]:3001"},
	} {
		got, err := agentOSManagerDestination(test.value)
		if err != nil || got != test.want {
			t.Fatalf("agentOSManagerDestination(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}

	for _, value := range []string{"", "https://127.0.0.1:3001", "http://10.0.2.2:3001", "http://127.0.0.1:3001/api", "http://user@127.0.0.1:3001"} {
		if _, err := agentOSManagerDestination(value); err == nil {
			t.Fatalf("agentOSManagerDestination(%q) unexpectedly succeeded", value)
		}
	}
}

func TestAgentNetworkRelayUsesPerAgentSocketIdentity(t *testing.T) {
	root := t.TempDir()
	first := agentNetworkRelaySocketPath(root, "guild-1/agent-1")
	second := agentNetworkRelaySocketPath(root, "guild-1/agent-2")

	if first == second {
		t.Fatalf("per-agent socket paths collided: %s", first)
	}
	if filepath.Dir(filepath.Dir(first)) != root || filepath.Dir(filepath.Dir(second)) != root {
		t.Fatalf("socket paths escaped relay root: %s %s", first, second)
	}
	if first != agentNetworkRelaySocketPath(root, "guild-1/agent-1") {
		t.Fatal("socket identity is not stable for the same agent")
	}
}

func TestAgentNetworkRelayDeniesBeforeAttemptingEgress(t *testing.T) {
	relay := &AgentNetworkRelay{allowed: []string{"api.openai.com"}}
	request := httptest.NewRequest(http.MethodGet, "http://metadata.internal/latest", nil)
	response := httptest.NewRecorder()

	relay.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestConfiguredUpstreamProxyUsesHTTPSProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")

	proxy := configuredUpstreamProxy()
	if proxy == nil || proxy.String() != "http://127.0.0.1:3128" {
		t.Fatalf("configured upstream = %v", proxy)
	}
}
