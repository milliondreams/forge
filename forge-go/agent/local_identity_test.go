package agent

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("FORGE_LOCAL_USER_ID", "test-user")
	_ = os.Setenv("FORGE_LOCAL_USER_NAME", "Test User")
	_ = os.Setenv("FORGE_LOCAL_ORGANIZATION_ID", "test-org")
	_ = os.Setenv("FORGE_LOCAL_ORGANIZATION_NAME", "Test Organization")
	_ = os.Setenv("FORGE_AGENT_REGISTRY", "../conf/forge-agent-registry.yaml")
	_ = os.Setenv("FORGE_DEPENDENCY_CONFIG", "../conf/agent-dependencies.yaml")
	_ = os.Setenv("FORGE_OAUTH_PROVIDERS_CONFIG", "../conf/oauth-providers.yaml")
	os.Exit(m.Run())
}
