package guild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_AssignsMissingAgentIDs(t *testing.T) {
	spec := &protocol.GuildSpec{
		ID:          "g-1",
		Name:        "Guild",
		Description: "Guild description",
		Agents: []protocol.AgentSpec{
			{
				ID:          "",
				Name:        "Echo Agent",
				Description: "Echo",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
			},
			{
				ID:          "custom-agent-id",
				Name:        "Helper Agent",
				Description: "Helper",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
			},
		},
	}

	applyDefaults(spec)

	require.Equal(t, "g-1#a-0", spec.Agents[0].ID)
	require.Equal(t, "custom-agent-id", spec.Agents[1].ID)
}

func TestBuildModels_PreservesDependencyMapsAndPredicates(t *testing.T) {
	spec := &protocol.GuildSpec{
		ID:          "g-1",
		Name:        "Guild",
		Description: "Guild description",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{
						"host": "redis",
						"port": "6379",
						"db":   0,
					},
				},
			},
		},
		DependencyMap: map[string]protocol.DependencySpec{
			"llm": {
				ClassName: "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver",
				Properties: map[string]interface{}{
					"model": "gpt-4o-mini",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "g-1#a-0",
				Name:        "Echo Agent",
				Description: "Echo",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"protocol": "file",
						},
					},
				},
				Predicates: map[string]protocol.RuntimePredicate{
					"on_message": {PredicateType: protocol.PredicateJSONata, Expression: strPtr("true")},
				},
			},
		},
	}

	gm, agents := buildModels(spec, "org-1")

	require.Equal(t, "rustic_ai.redis.messaging.backend", gm.BackendModule)
	require.Equal(t, "RedisMessagingBackend", gm.BackendClass)
	require.Contains(t, gm.BackendConfig, "redis_client")
	require.Contains(t, gm.DependencyMap, "llm")
	llmEntry, ok := gm.DependencyMap["llm"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver", llmEntry["class_name"])

	require.Len(t, agents, 1)
	require.Contains(t, agents[0].DependencyMap, "filesystem")
	fsEntry, ok := agents[0].DependencyMap["filesystem"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver", fsEntry["class_name"])
	require.Contains(t, agents[0].Predicates, "on_message")
}

func TestBuildModels_PersistsRoutes(t *testing.T) {
	routeTimes := 1
	spec := &protocol.GuildSpec{
		ID:          "g-routes",
		Name:        "Guild Routes",
		Description: "Guild with routing",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{
				{
					AgentType:  strPtr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: strPtr("unwrap_and_forward_message"),
					Destination: &protocol.RoutingDestination{
						Topics: protocol.TopicsFromSlice([]string{"echo_topic"}),
					},
					RouteTimes: &routeTimes,
				},
			},
		},
	}

	gm, _ := buildModels(spec, "org-1")

	require.Len(t, gm.Routes, 1)
	require.NotNil(t, gm.Routes[0].GuildID)
	require.Equal(t, "g-routes", *gm.Routes[0].GuildID)
	require.Equal(t, "unwrap_and_forward_message", *gm.Routes[0].MethodName)
	require.Equal(t, []string{"echo_topic"}, []string(gm.Routes[0].DestinationTopics))
}

func TestBuildModels_PreservesAgentBehaviorFlags(t *testing.T) {
	listenToDefault := true
	actOnlyWhenTagged := true
	spec := &protocol.GuildSpec{
		ID:          "g-agent-flags",
		Name:        "Agent Flags",
		Description: "Guild with explicit agent behavior flags",
		Properties:  map[string]interface{}{},
		Agents: []protocol.AgentSpec{
			{
				ID:                   "echo",
				Name:                 "Echo Agent",
				Description:          "Echo",
				ClassName:            "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				ListenToDefaultTopic: &listenToDefault,
				ActOnlyWhenTagged:    &actOnlyWhenTagged,
			},
		},
	}

	_, agents := buildModels(spec, "org-1")

	require.Len(t, agents, 1)
	require.True(t, agents[0].ListenToDefaultTopic)
	require.True(t, agents[0].ActOnlyWhenTagged)
}

func TestNormalizeRuntimeSpecIDs_AssignsGuildScopedDefaults(t *testing.T) {
	spec := &protocol.GuildSpec{
		Name:        "Guild",
		Description: "Guild description",
		Agents: []protocol.AgentSpec{
			{ID: "a-0", Name: "A"},
			{ID: "", Name: "B"},
			{ID: "custom", Name: "C"},
		},
	}

	normalizeRuntimeSpecIDs(spec, "g-123")

	require.Equal(t, "g-123", spec.ID)
	require.Equal(t, "g-123#a-0", spec.Agents[0].ID)
	require.Equal(t, "g-123#a-1", spec.Agents[1].ID)
	require.Equal(t, "custom", spec.Agents[2].ID)
}

func TestNormalizeAgentModelIDs_AssignsGuildScopedDefaults(t *testing.T) {
	models := []store.AgentModel{
		{ID: "a-0"},
		{ID: ""},
		{ID: "custom"},
	}

	normalizeAgentModelIDs(models, "g-123")

	require.Equal(t, "g-123#a-0", models[0].ID)
	require.Equal(t, "g-123#a-1", models[1].ID)
	require.Equal(t, "custom", models[2].ID)
}

func TestMergeDependencies_MissingConfigIsNoop(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"llm": {
				ClassName: "custom.llm.Resolver",
				Properties: map[string]interface{}{
					"model": "custom-model",
				},
			},
		},
	}

	err := mergeDependencies(spec, filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	require.NoError(t, err)

	// Missing file should be a no-op.
	require.Equal(t, "custom.llm.Resolver", spec.DependencyMap["llm"].ClassName)
	require.Len(t, spec.DependencyMap, 1)
}

func TestMergeDependencies_LoadsClassNameFromYAML(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{},
	}

	cfg := []byte(`
filesystem:
  class_name: rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver
  properties:
    path_base: /tmp
`)
	configPath := filepath.Join(t.TempDir(), "deps.yaml")
	require.NoError(t, os.WriteFile(configPath, cfg, 0o644))

	err := mergeDependencies(spec, configPath)
	require.NoError(t, err)

	require.Contains(t, spec.DependencyMap, "filesystem")
	require.Equal(
		t,
		"rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
		spec.DependencyMap["filesystem"].ClassName,
	)
}

func TestApplyFilesystemGlobalRoot_RewritesPathBaseOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "/uploads",
					"protocol":  "file",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "uploads"), spec.DependencyMap["filesystem"].Properties["path_base"])

	err = ApplyFilesystemGlobalRoot(spec, root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "uploads"), spec.DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_UsesRootWhenPathBaseMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName:  "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, root)
	require.NoError(t, err)
	require.Equal(t, root, spec.DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_RejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "../escape",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, root)
	require.ErrorContains(t, err, "path traversal")
}

func TestApplyFilesystemGlobalRoot_RewritesS3PathBaseAndProtocol(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "s3", spec.DependencyMap["filesystem"].Properties["protocol"])
	require.Equal(t, "s3://forge-bucket/root/uploads", spec.DependencyMap["filesystem"].Properties["path_base"])

	err = ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "s3://forge-bucket/root/uploads", spec.DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_RewritesGCSPathBaseAndProtocol(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "/uploads",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "gs://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "gs", spec.DependencyMap["filesystem"].Properties["protocol"])
	require.Equal(t, "gs://forge-bucket/root/uploads", spec.DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_RewritesAgentLevelDependency(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	spec := &protocol.GuildSpec{
		Agents: []protocol.AgentSpec{
			{
				ID: "g-1#a-0",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, root)
	require.NoError(t, err)
	require.Equal(t, "file", spec.Agents[0].DependencyMap["filesystem"].Properties["protocol"])
	require.Equal(t, filepath.Join(root, "private"), spec.Agents[0].DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_RewritesAgentLevelObjectStoreDependency(t *testing.T) {
	spec := &protocol.GuildSpec{
		Agents: []protocol.AgentSpec{
			{
				ID: "g-1#a-0",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "s3", spec.Agents[0].DependencyMap["filesystem"].Properties["protocol"])
	require.Equal(t, "s3://forge-bucket/root/private", spec.Agents[0].DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_AgentLevelIsIdempotent(t *testing.T) {
	spec := &protocol.GuildSpec{
		Agents: []protocol.AgentSpec{
			{
				ID: "g-1#a-0",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "s3://forge-bucket/root/private",
							"protocol":  "s3",
						},
					},
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "s3://forge-bucket/root/private", spec.Agents[0].DependencyMap["filesystem"].Properties["path_base"])
}

func TestApplyFilesystemGlobalRoot_AgentLevelRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	spec := &protocol.GuildSpec{
		Agents: []protocol.AgentSpec{
			{
				ID: "g-1#a-0",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "../private",
						},
					},
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, root)
	require.ErrorContains(t, err, "path traversal")
}

func TestApplyFilesystemGlobalRoot_PreservesMatchingQualifiedObjectURL(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "s3://forge-bucket/root/uploads",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.NoError(t, err)
	require.Equal(t, "s3://forge-bucket/root/uploads", spec.DependencyMap["filesystem"].Properties["path_base"])
	require.Equal(t, "s3", spec.DependencyMap["filesystem"].Properties["protocol"])
}

func TestApplyFilesystemGlobalRoot_RejectsObjectStoreSchemeMismatch(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "gs://forge-bucket/root/uploads",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.ErrorContains(t, err, "does not match Forge global root scheme")
}

func TestApplyFilesystemGlobalRoot_RejectsObjectStoreBucketMismatch(t *testing.T) {
	spec := &protocol.GuildSpec{
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "s3://other-bucket/root/uploads",
				},
			},
		},
	}

	err := ApplyFilesystemGlobalRoot(spec, "s3://forge-bucket/root")
	require.ErrorContains(t, err, "does not match Forge global root bucket")
}

func TestApplyDefaults_InjectsStateManagerFromEnv(t *testing.T) {
	t.Setenv("RUSTIC_AI_STATE_MANAGER", "rustic_ai.core.state.manager.diskcache_state_manager.DiskCacheStateManager")

	spec := &protocol.GuildSpec{
		ID:   "g-sm",
		Name: "Guild",
	}

	applyDefaults(spec)

	require.Equal(t, "rustic_ai.core.state.manager.diskcache_state_manager.DiskCacheStateManager", spec.Properties["state_manager"])
}

func TestApplyDefaults_DoesNotOverrideExistingStateManager(t *testing.T) {
	t.Setenv("RUSTIC_AI_STATE_MANAGER", "rustic_ai.core.state.manager.diskcache_state_manager.DiskCacheStateManager")

	spec := &protocol.GuildSpec{
		ID:   "g-sm",
		Name: "Guild",
		Properties: map[string]interface{}{
			"state_manager": "my.custom.StateManager",
		},
	}

	applyDefaults(spec)

	require.Equal(t, "my.custom.StateManager", spec.Properties["state_manager"])
}

func TestApplyStateManagerConfig_InjectsCacheDir(t *testing.T) {
	spec := &protocol.GuildSpec{
		Properties: map[string]interface{}{
			"state_manager": "rustic_ai.core.state.manager.diskcache_state_manager.DiskCacheStateManager",
		},
	}

	applyStateManagerConfig(spec, "org-1", "guild-42")

	cfg, ok := spec.Properties["state_manager_config"].(map[string]interface{})
	require.True(t, ok, "state_manager_config should be a map")
	cacheDir, ok := cfg["cache_dir"].(string)
	require.True(t, ok, "cache_dir should be a string")
	require.True(t, strings.Contains(cacheDir, filepath.Join("state_stores", "org-1", "guild-42")),
		"cache_dir should contain state_stores/org-1/guild-42, got %s", cacheDir)
}

func TestApplyStateManagerConfig_SkipsNonDiskCache(t *testing.T) {
	spec := &protocol.GuildSpec{
		Properties: map[string]interface{}{
			"state_manager": "some.other.StateManager",
		},
	}

	applyStateManagerConfig(spec, "org-1", "guild-42")

	require.Nil(t, spec.Properties["state_manager_config"])
}

func TestApplyStateManagerConfig_DoesNotOverrideExistingConfig(t *testing.T) {
	spec := &protocol.GuildSpec{
		Properties: map[string]interface{}{
			"state_manager": "rustic_ai.core.state.manager.diskcache_state_manager.DiskCacheStateManager",
			"state_manager_config": map[string]interface{}{
				"cache_dir": "/custom/path",
			},
		},
	}

	applyStateManagerConfig(spec, "org-1", "guild-42")

	cfg := spec.Properties["state_manager_config"].(map[string]interface{})
	require.Equal(t, "/custom/path", cfg["cache_dir"])
}
