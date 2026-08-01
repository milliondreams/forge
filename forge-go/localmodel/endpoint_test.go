package localmodel

import (
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestApplyDependencyOverrideTargetsOnlyRusticLocalModels(t *testing.T) {
	t.Setenv(BaseURLEnv, "http://10.0.2.101:55262/v1/")
	dependencies := map[string]protocol.DependencySpec{
		"llm": {
			Properties: map[string]any{
				"model": "openai/rustic/local",
				"conf":  map[string]any{"base_url": "http://localhost:55262/v1"},
			},
		},
		"llm_openai": {
			Properties: map[string]any{
				"model": "gpt-5.4",
				"conf":  map[string]any{"base_url": "https://api.openai.com/v1"},
			},
		},
	}

	if err := ApplyDependencyOverride(dependencies); err != nil {
		t.Fatalf("ApplyDependencyOverride: %v", err)
	}

	localConf := dependencies["llm"].Properties["conf"].(map[string]any)
	if got := localConf["base_url"]; got != "http://10.0.2.101:55262/v1" {
		t.Fatalf("local base_url = %v", got)
	}
	cloudConf := dependencies["llm_openai"].Properties["conf"].(map[string]any)
	if got := cloudConf["base_url"]; got != "https://api.openai.com/v1" {
		t.Fatalf("cloud base_url changed to %v", got)
	}
}

func TestEndpointOverrideRejectsCredentials(t *testing.T) {
	t.Setenv(BaseURLEnv, "http://token@example.test/v1")
	if _, _, err := EndpointOverride(); err == nil {
		t.Fatal("expected endpoint credentials to be rejected")
	}
}
