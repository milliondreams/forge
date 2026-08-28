package supervisor

import (
	"strings"
	"testing"
)

func TestAgentProcessEnvironmentFiltersUndeclaredCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "parent-openai")
	t.Setenv("GITHUB_TOKEN", "parent-github")
	t.Setenv("FORGE_TEST_SETTING", "preserved")

	environment := agentProcessEnvironment([]string{"GITHUB_TOKEN=resolved-github", "FORGE_AGENT_CONFIG_JSON={}"})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "OPENAI_API_KEY=") {
		t.Fatal("undeclared parent credential leaked into agent environment")
	}
	if !strings.Contains(joined, "GITHUB_TOKEN=resolved-github") {
		t.Fatal("declared resolved credential was not injected")
	}
	if !strings.Contains(joined, "FORGE_TEST_SETTING=preserved") {
		t.Fatal("ordinary parent environment was not preserved")
	}
}
