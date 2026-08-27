package api

import (
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/stretchr/testify/require"
)

const (
	baseKVStoreType  = "rustic_ai.core.guild.agent_ext.depends.kvstore.base.BaseKVStore"
	fileSystemType   = "fsspec.implementations.dirfs.DirFileSystem"
	codeRunnerType   = "rustic_ai.core.guild.agent_ext.depends.code_execution.stateless.code_runner.CodeRunner"
	embeddingsType   = "rustic_ai.core.guild.agent_ext.depends.embeddings.embeddings.Embeddings"
	vectorStoreType  = "rustic_ai.core.guild.agent_ext.depends.vectorstore.vectorstore.VectorStore"
	textSplitterType = "rustic_ai.core.guild.agent_ext.depends.text_splitter.text_splitter.TextSplitter"
	kbBackendType    = "rustic_ai.core.knowledgebase.kbindex_backend.KBIndexBackend"
	llmType          = "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM"
)

func TestBundledDependencyProfilesUseCanonicalTypes(t *testing.T) {
	profiles, err := loadConfiguredDependencyProfiles(filepath.Join("..", "conf", "agent-dependencies.yaml"))
	require.NoError(t, err)

	expected := map[string]string{
		"kvstore":                baseKVStoreType,
		"filesystem":             fileSystemType,
		"code_runner":            codeRunnerType,
		"embeddings":             embeddingsType,
		"embeddings_local_nomic": embeddingsType,
		"vectorstore":            vectorStoreType,
		"textsplitter":           textSplitterType,
		"kb_backend":             kbBackendType,
	}
	for key, profile := range profiles {
		require.NotEmpty(t, profile.ProvidedType, "profile %q must declare provided_type", key)
		require.NotNil(t, profile.Catalog.Selectable, "profile %q must explicitly declare catalog.selectable", key)
	}
	for key, providedType := range expected {
		require.Contains(t, profiles, key)
		require.Equal(t, providedType, profiles[key].ProvidedType, "profile %q", key)
	}

	nomic := profiles["embeddings_local_nomic"]
	require.Equal(t, "rustic_ai.langchain.agent_ext.embeddings.openai.OpenAIEmbeddingsResolver", nomic.ClassName)
	require.Equal(t, "rustic/nomic-embed-default", nomic.Properties["model_name"])
	require.Contains(t, nomic.Catalog.Capabilities, "embeddings")
	require.NotContains(t, profiles, "llm_local_nomic_embed")

	for key, profile := range profiles {
		if key == "llm" || len(key) > 4 && key[:4] == "llm_" {
			require.Equal(t, llmType, profile.ProvidedType, "chat profile %q", key)
		}
	}
}

func TestLegacyExactDependencyMatchAddsOnlyUniqueRequirements(t *testing.T) {
	agent := protocol.NewAgentSpec()
	agent.DependencyMap["llm"] = protocol.DependencySpec{
		ClassName:  "example.Resolver",
		Properties: map[string]interface{}{"model": "hosted"},
	}
	profiles := map[string]configuredDependencyProfile{
		"hosted": {
			ClassName: "example.Resolver", ProvidedType: llmType,
			Properties:   map[string]interface{}{"model": "hosted"},
			Requirements: dependencyRequirements{Secrets: []string{"HOSTED_API_KEY"}},
		},
	}
	enrichAgentDependencyRequirements(&agent, profiles)
	require.Equal(t, []string{"HOSTED_API_KEY"}, agent.Resources.Secrets)
	require.Equal(t, []string{"hosted"}, agent.Properties[protocol.DependencyProfilesProperty])

	profiles["ambiguous"] = profiles["hosted"]
	agent.Resources.Secrets = nil
	delete(agent.Properties, protocol.DependencyProfilesProperty)
	enrichAgentDependencyRequirements(&agent, profiles)
	require.Empty(t, agent.Resources.Secrets, "ambiguous legacy profiles must not be guessed")
}

func TestNomicProfileIsReturnedOnlyForEmbeddings(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-affect-readiness")
	configPath := filepath.Join("..", "conf", "agent-dependencies.yaml")
	embeddingProfiles, err := safeConfiguredDependencyEntries(configPath, embeddingsType, "", false)
	require.NoError(t, err)
	require.Contains(t, dependencyEntryKeys(embeddingProfiles), "embeddings_local_nomic")

	llmProfiles, err := safeConfiguredDependencyEntries(configPath, llmType, "", false)
	require.NoError(t, err)
	require.NotContains(t, dependencyEntryKeys(llmProfiles), "embeddings_local_nomic")

	allEmbeddingProfiles, err := safeConfiguredDependencyEntries(configPath, embeddingsType, "", true)
	require.NoError(t, err)
	require.Contains(t, dependencyEntryKeys(allEmbeddingProfiles), "embeddings")
	for _, entry := range allEmbeddingProfiles {
		if entry.Key == "embeddings" {
			require.Equal(t, "needs_configuration", entry.Availability.Status)
		}
	}
}

func TestSecretDependentProfileReadinessComesOnlyFromNodes(t *testing.T) {
	configPath := filepath.Join("..", "conf", "agent-dependencies.yaml")
	t.Setenv("OPENAI_API_KEY", "ignored")

	entries, err := safeConfiguredDependencyEntries(configPath, embeddingsType, "", false)
	require.NoError(t, err)
	require.NotContains(t, dependencyEntryKeys(entries), "embeddings")

	scheduler.GlobalNodeRegistry.RegisterWithReadiness("dependency-profile-test", scheduler.ResourceCapacity{CPUs: 1, Memory: 1}, []string{"embeddings"})
	t.Cleanup(func() { scheduler.GlobalNodeRegistry.Deregister("dependency-profile-test") })
	entries, err = safeConfiguredDependencyEntries(configPath, embeddingsType, "", false)
	require.NoError(t, err)
	require.Contains(t, dependencyEntryKeys(entries), "embeddings")
}

func TestEveryCanonicalTypeCanBeQueried(t *testing.T) {
	configPath := filepath.Join("..", "conf", "agent-dependencies.yaml")
	expected := map[string]string{
		baseKVStoreType:  "kvstore",
		fileSystemType:   "filesystem",
		codeRunnerType:   "code_runner",
		embeddingsType:   "embeddings_local_nomic",
		vectorStoreType:  "vectorstore",
		textSplitterType: "textsplitter",
		kbBackendType:    "kb_backend",
		llmType:          "llm",
	}
	for providedType, expectedKey := range expected {
		entries, err := safeConfiguredDependencyEntries(configPath, providedType, "", true)
		require.NoError(t, err, "provided_type %q", providedType)
		require.Contains(t, dependencyEntryKeys(entries), expectedKey, "provided_type %q", providedType)
	}
}

func dependencyEntryKeys(entries []ConfiguredDependencyEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}
