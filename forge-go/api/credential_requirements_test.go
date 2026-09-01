package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/stretchr/testify/require"
)

func TestResolveAgentCredentialRequirementsUsesOnlyRegistryAndSelectedProfiles(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	require.NoError(t, os.WriteFile(registryPath, []byte(`entries:
  - id: SERPAgent
    class_name: test.SERPAgent
    runtime: uvx
    requirements:
      secrets:
        - key: SERP_API_KEY
          env: SERP_API_KEY
          label: SERP API Key
  - id: LLMAgent
    class_name: test.LLMAgent
    runtime: uvx
`), 0o600))
	reg, err := registry.Load(registryPath, nil)
	require.NoError(t, err)

	profilesPath := filepath.Join(dir, "dependencies.yaml")
	require.NoError(t, os.WriteFile(profilesPath, []byte(`llm_openai:
  class_name: test.LLMResolver
  provided_type: test.LLM
  requirements:
    secrets:
      - key: OPENAI_API_KEY
        env: OPENAI_API_KEY
        label: OpenAI API Key
  properties: {model: openai}
llm_local:
  class_name: test.LLMResolver
  provided_type: test.LLM
  properties: {model: local}
`), 0o600))

	serp := protocol.NewAgentSpec()
	serp.ID, serp.Name, serp.ClassName = "serp", "Search", "test.SERPAgent"
	serp.Resources.Secrets = []string{"MUST_NOT_BE_USED"}
	resolved, err := ResolveAgentCredentialRequirements(&serp, reg, profilesPath)
	require.NoError(t, err)
	require.Len(t, resolved.Secrets, 1)
	require.Equal(t, "SERP_API_KEY", resolved.Secrets[0].Key)

	llm := protocol.NewAgentSpec()
	llm.ID, llm.Name, llm.ClassName = "llm", "LLM", "test.LLMAgent"
	llm.Properties[protocol.DependencyProfilesProperty] = []string{"llm_openai"}
	resolved, err = ResolveAgentCredentialRequirements(&llm, reg, profilesPath)
	require.NoError(t, err)
	require.Len(t, resolved.Secrets, 1)
	require.Equal(t, "OPENAI_API_KEY", resolved.Secrets[0].Key)

	llm.Properties[protocol.DependencyProfilesProperty] = []string{"llm_local"}
	resolved, err = ResolveAgentCredentialRequirements(&llm, reg, profilesPath)
	require.NoError(t, err)
	require.Empty(t, resolved.Secrets)
}

func TestResolveAgentCredentialRequirementsRejectsConflictingDeclarations(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	require.NoError(t, os.WriteFile(registryPath, []byte(`entries:
  - id: Agent
    class_name: test.Agent
    runtime: uvx
    requirements:
      secrets:
        - key: SHARED_KEY
          env: SHARED_KEY
          label: Shared Key
`), 0o600))
	reg, err := registry.Load(registryPath, nil)
	require.NoError(t, err)
	profilesPath := filepath.Join(dir, "dependencies.yaml")
	require.NoError(t, os.WriteFile(profilesPath, []byte(`profile:
  class_name: test.Resolver
  provided_type: test.Type
  requirements:
    secrets:
      - key: SHARED_KEY
        env: OTHER_ENV
        label: Shared Key
  properties: {}
`), 0o600))

	agent := protocol.NewAgentSpec()
	agent.ID, agent.Name, agent.ClassName = "agent", "Agent", "test.Agent"
	agent.Properties[protocol.DependencyProfilesProperty] = []string{"profile"}
	_, err = ResolveAgentCredentialRequirements(&agent, reg, profilesPath)
	require.ErrorContains(t, err, "conflicting declarations")
}

func TestResolveAgentCredentialRequirementsDeduplicatesAndUnionsSources(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	require.NoError(t, os.WriteFile(registryPath, []byte(`entries:
  - id: Agent
    class_name: test.Agent
    runtime: binary
    requirements:
      secrets:
        - key: SHARED_KEY
          env: SHARED_KEY
          label: Shared Key
          optional: true
      oauth:
        - provider: google
          env: GOOGLE_ACCESS_TOKEN
          label: Google Account
          scopes: [scope.one]
          optional: true
`), 0o600))
	reg, err := registry.Load(registryPath, nil)
	require.NoError(t, err)
	profilesPath := filepath.Join(dir, "dependencies.yaml")
	require.NoError(t, os.WriteFile(profilesPath, []byte(`profile:
  class_name: test.Resolver
  provided_type: test.Type
  requirements:
    secrets:
      - key: SHARED_KEY
        env: SHARED_KEY
        label: Shared Key
        optional: false
    oauth:
      - provider: google
        env: GOOGLE_ACCESS_TOKEN
        label: Google Account
        scopes: [scope.two, scope.one]
  properties: {}
`), 0o600))

	agent := protocol.NewAgentSpec()
	agent.ID, agent.Name, agent.ClassName = "agent", "Agent", "test.Agent"
	agent.Properties[protocol.DependencyProfilesProperty] = []string{"profile"}
	resolved, err := resolveGuildCredentialRequirements(&protocol.GuildSpec{Agents: []protocol.AgentSpec{agent}}, reg, profilesPath)
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Len(t, resolved[0].Secrets, 1)
	require.False(t, isOptional(resolved[0].Secrets[0].Need.Optional), "a mandatory declaration must dominate")
	require.Len(t, resolved[0].Secrets[0].Sources, 2)
	require.Len(t, resolved[0].OAuth, 1)
	require.False(t, isOptional(resolved[0].OAuth[0].Need.Optional), "a mandatory declaration must dominate")
	require.Equal(t, []string{"scope.one", "scope.two"}, resolved[0].OAuth[0].Need.Scopes)
	require.Len(t, resolved[0].OAuth[0].Sources, 2)
}
