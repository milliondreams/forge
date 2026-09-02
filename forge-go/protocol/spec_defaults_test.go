package protocol

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewGuildSpec_DefaultParity(t *testing.T) {
	spec := NewGuildSpec()

	if spec.ID == "" {
		t.Fatalf("expected guild id default")
	}
	if spec.Properties == nil {
		t.Fatalf("expected properties default map")
	}
	if spec.Agents == nil {
		t.Fatalf("expected agents default slice")
	}
	if spec.DependencyMap == nil {
		t.Fatalf("expected dependency_map default map")
	}
	if spec.Routes == nil {
		t.Fatalf("expected routes default routing slip")
	}
	if spec.Routes.Steps == nil {
		t.Fatalf("expected routes.steps default slice")
	}
}

func TestGuildSpecUnmarshal_Defaults(t *testing.T) {
	raw := []byte(`{
		"name":"Simple Echo",
		"description":"echo",
		"agents":[{"name":"Echo Agent","description":"echo","class_name":"rustic_ai.core.agents.testutils.echo_agent.EchoAgent"}]
	}`)

	var spec GuildSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unmarshal guild spec: %v", err)
	}

	if spec.ID == "" {
		t.Fatalf("expected guild id default")
	}
	if spec.Properties == nil || spec.DependencyMap == nil {
		t.Fatalf("expected non-nil guild maps")
	}
	if spec.Routes == nil || spec.Routes.Steps == nil {
		t.Fatalf("expected default routing slip")
	}
	if len(spec.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(spec.Agents))
	}

	agent := spec.Agents[0]
	if agent.ID == "" {
		t.Fatalf("expected agent id default")
	}
	if agent.AdditionalTopics == nil || agent.Properties == nil {
		t.Fatalf("expected default agent list/map fields")
	}
	if agent.Predicates == nil || agent.DependencyMap == nil {
		t.Fatalf("expected default agent predicate/dependency maps")
	}
	if agent.AdditionalDependencies == nil {
		t.Fatalf("expected default additional_dependencies")
	}
	if agent.ListenToDefaultTopic == nil || !*agent.ListenToDefaultTopic {
		t.Fatalf("expected listen_to_default_topic default true")
	}
	if agent.ActOnlyWhenTagged == nil || *agent.ActOnlyWhenTagged {
		t.Fatalf("expected act_only_when_tagged default false")
	}
	if agent.Resources.CustomResources == nil {
		t.Fatalf("expected default resource custom_resources")
	}
}

func TestGatewayConfigUnmarshal_EnabledDefaultTrue(t *testing.T) {
	raw := []byte(`{"input_formats":["f1"],"output_formats":["f2"]}`)

	var cfg GatewayConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal gateway config: %v", err)
	}

	if !cfg.Enabled {
		t.Fatalf("expected enabled default true")
	}
	if cfg.ReturnedFormats == nil {
		t.Fatalf("expected returned_formats default slice")
	}
}

func TestRoutingRuleUnmarshal_DefaultRouteTimes(t *testing.T) {
	raw := []byte(`{"agent_type":"x","method_name":"y"}`)

	var rule RoutingRule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatalf("unmarshal routing rule: %v", err)
	}

	if rule.RouteTimes == nil || *rule.RouteTimes != 1 {
		t.Fatalf("expected route_times default 1")
	}
}

func TestNeedsSpec_Defaults(t *testing.T) {
	needs := NewNeedsSpec()

	if needs.Secrets == nil {
		t.Fatalf("expected secrets default slice")
	}
	if needs.OAuth == nil {
		t.Fatalf("expected oauth default slice")
	}
	if needs.Capabilities == nil {
		t.Fatalf("expected capabilities default slice")
	}
	if needs.Network.Allow == nil {
		t.Fatalf("expected network.allow default slice")
	}
	if needs.Filesystem.Allow == nil {
		t.Fatalf("expected filesystem.allow default slice")
	}
}

func TestAgentNeedsUnmarshal_Defaults(t *testing.T) {
	raw := []byte(`{
		"class_name":"  rustic_ai.browser.agent.BrowserAgent  ",
		"needs":{
			"secrets":[{"key":" OPENAI_API_KEY "}],
			"oauth":[{"provider":" google "}],
			"capabilities":[{"type":" filesystem "}],
			"network":{"allow":[" api.openai.com "]},
			"filesystem":{"allow":[{"path":" /tmp/project ","mode":" rw "}]}
		}
	}`)

	var needs AgentNeeds
	if err := json.Unmarshal(raw, &needs); err != nil {
		t.Fatalf("unmarshal agent needs: %v", err)
	}

	if needs.ClassName != "rustic_ai.browser.agent.BrowserAgent" {
		t.Fatalf("expected trimmed class name, got %q", needs.ClassName)
	}
	if len(needs.Needs.Secrets) != 1 || needs.Needs.Secrets[0].Key != "OPENAI_API_KEY" {
		t.Fatalf("expected normalized secret key")
	}
	if needs.Needs.Secrets[0].Optional != nil {
		t.Fatalf("expected secret optional by default (nil), got non-nil")
	}
	if len(needs.Needs.OAuth) != 1 || needs.Needs.OAuth[0].Provider != "google" {
		t.Fatalf("expected normalized oauth provider")
	}
	if needs.Needs.OAuth[0].Label != "google" {
		t.Fatalf("expected oauth label to default to provider, got %q", needs.Needs.OAuth[0].Label)
	}
	if needs.Needs.OAuth[0].Optional != nil {
		t.Fatalf("expected oauth optional by default (nil), got non-nil")
	}
	if len(needs.Needs.Capabilities) != 1 || needs.Needs.Capabilities[0].Type != "filesystem" {
		t.Fatalf("expected normalized capability type")
	}
	if len(needs.Needs.Network.Allow) != 1 || needs.Needs.Network.Allow[0] != "api.openai.com" {
		t.Fatalf("expected normalized network allow")
	}
	if len(needs.Needs.Filesystem.Allow) != 1 {
		t.Fatalf("expected filesystem allow entry")
	}
	if needs.Needs.Filesystem.Allow[0].Path != "/tmp/project" || needs.Needs.Filesystem.Allow[0].Mode != "rw" {
		t.Fatalf("expected normalized filesystem need")
	}
}

func TestOAuthNeedUnmarshalYAML_Defaults(t *testing.T) {
	raw := []byte(`
class_name: rustic_ai.browser.agent.BrowserAgent
needs:
  oauth:
    - provider: " github "
    - provider: gitlab
      label: MY_GITLAB_TOKEN
      scopes:
        - read_api
`)

	var needs AgentNeeds
	if err := yaml.Unmarshal(raw, &needs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(needs.Needs.OAuth) != 2 {
		t.Fatalf("expected 2 oauth entries, got %d", len(needs.Needs.OAuth))
	}

	gh := needs.Needs.OAuth[0]
	if gh.Provider != "github" {
		t.Errorf("expected trimmed provider 'github', got %q", gh.Provider)
	}
	if gh.Label != "github" {
		t.Errorf("expected label defaulted to provider, got %q", gh.Label)
	}
	if gh.Scopes == nil {
		t.Errorf("expected non-nil scopes slice")
	}
	if gh.Optional != nil {
		t.Errorf("expected optional nil by default")
	}

	gl := needs.Needs.OAuth[1]
	if gl.Label != "MY_GITLAB_TOKEN" {
		t.Errorf("expected explicit label MY_GITLAB_TOKEN, got %q", gl.Label)
	}
	if len(gl.Scopes) != 1 || gl.Scopes[0] != "read_api" {
		t.Errorf("expected scopes [read_api], got %v", gl.Scopes)
	}
}
