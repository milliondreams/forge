package guild

import (
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestApplyLocalModelEndpointOverrideUpdatesGuildAndAgentDependencies(t *testing.T) {
	t.Setenv("AGENTOS_LOCAL_MODEL_BASE_URL", "http://10.0.2.101:55262/v1")
	localDependency := protocol.DependencySpec{
		Properties: map[string]any{
			"model": "openai/rustic/test",
			"conf":  map[string]any{"base_url": "http://localhost:55262/v1"},
		},
	}
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{"llm": localDependency},
		Agents: []protocol.AgentSpec{
			{DependencyMap: map[string]protocol.DependencySpec{"llm_local_test": localDependency}},
		},
	}

	if err := applyLocalModelEndpointOverride(spec); err != nil {
		t.Fatalf("applyLocalModelEndpointOverride: %v", err)
	}
	for _, dependency := range []protocol.DependencySpec{
		spec.DependencyMap["llm"],
		spec.Agents[0].DependencyMap["llm_local_test"],
	} {
		conf := dependency.Properties["conf"].(map[string]any)
		if got := conf["base_url"]; got != "http://10.0.2.101:55262/v1" {
			t.Fatalf("base_url = %v", got)
		}
	}
}
