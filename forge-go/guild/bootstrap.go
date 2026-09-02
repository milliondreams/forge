package guild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"log/slog"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

func Bootstrap(ctx context.Context, db store.Store, pusher protocol.ControlPusher, infraPublisher *infraevents.Publisher, spec *protocol.GuildSpec, orgID, createdBy, dependencyConfigPath string) (*store.GuildModel, error) {
	applyDefaults(spec)

	// Dependency files are installation catalogs. Only materialize profiles
	// required by this guild; explicit spec-level dependencies remain authoritative.
	forgeHomeDeps := forgepath.Resolve(forgepath.DependencyConfigFile)
	if err := mergeRequiredDependencies(spec, db, forgeHomeDeps, dependencyConfigPath); err != nil {
		return nil, fmt.Errorf("failed to materialize dependencies: %w", err)
	}
	stripDependencyCatalogMetadata(spec)
	if err := ApplyFilesystemGlobalRoot(spec, strings.TrimSpace(os.Getenv(forgeFilesystemGlobalRootEnv))); err != nil {
		return nil, fmt.Errorf("failed to normalize filesystem dependency: %w", err)
	}

	guildModel, agentModels := buildModels(spec, orgID, createdBy)
	if err := ValidateID(guildModel.ID); err != nil {
		return nil, err
	}
	normalizeRuntimeSpecIDs(spec, guildModel.ID)
	normalizeAgentModelIDs(agentModels, guildModel.ID)
	applyStateManagerConfig(spec, orgID, guildModel.ID)

	if err := db.CreateGuildWithAgents(guildModel, agentModels); err != nil {
		return nil, fmt.Errorf("failed to persist guild and agents: %w", err)
	}

	_ = infraPublisher.Emit(ctx, infraevents.EmitParams{
		Kind:            "guild.launch.persisted",
		Severity:        infraevents.SeverityInfo,
		GuildID:         guildModel.ID,
		OrganizationID:  orgID,
		SourceComponent: "forge-go.guild-bootstrap",
		Message:         "guild metadata persisted",
	})

	if err := EnqueueGuildManagerSpawn(ctx, pusher, infraPublisher, spec, orgID, createdBy); err != nil {
		return nil, fmt.Errorf("failed to enqueue GMA spawn request: %w", err)
	}

	slog.Info("Guild bootstrap complete. Enqueued GuildManagerAgent.", "guild_id", guildModel.ID)

	return guildModel, nil
}

func EnqueueGuildManagerSpawn(ctx context.Context, pusher protocol.ControlPusher, infraPublisher *infraevents.Publisher, spec *protocol.GuildSpec, orgID, createdBy string) error {
	if spec == nil {
		return fmt.Errorf("guild spec is required")
	}
	if spec.ID == "" {
		return fmt.Errorf("guild spec id is required")
	}
	if strings.TrimSpace(createdBy) == "" {
		return fmt.Errorf("guild creator is required")
	}
	normalizeRuntimeSpecIDs(spec, spec.ID)

	specBytes, _ := json.Marshal(spec)
	managerAPIBaseURL := strings.TrimSpace(os.Getenv("FORGE_MANAGER_API_BASE_URL"))
	if managerAPIBaseURL == "" {
		managerAPIBaseURL = "http://127.0.0.1:9090"
	}
	managerAPIToken := strings.TrimSpace(os.Getenv("FORGE_MANAGER_API_TOKEN"))

	spawnReq := protocol.SpawnRequest{
		RequestID: "bootstrap-" + spec.ID,
		GuildID:   spec.ID,
		AgentSpec: protocol.AgentSpec{
			ID:          spec.ID + "#manager_agent",
			Name:        spec.Name + " Manager",
			Description: "System agent for guild lifecycle orchestration",
			ClassName:   GuildManagerClassName,
			AdditionalTopics: []string{
				"system_topic",
				"heartbeat_topic",
				"guild_status_topic",
			},
			ListenToDefaultTopic: boolPtr(false),
			Properties: map[string]interface{}{
				"guild_spec":           spec,
				"manager_api_base_url": managerAPIBaseURL,
				"organization_id":      orgID,
				"created_by":           createdBy,
				"manager_api_token":    managerAPIToken,
			},
		},
		ClientType: "forge",
		ClientProperties: protocol.JSONB{
			"guild_spec":           string(specBytes),
			"manager_api_base_url": managerAPIBaseURL,
			"organization_id":      orgID,
			"created_by":           createdBy,
		},
	}

	_ = infraPublisher.Emit(ctx, infraevents.EmitParams{
		Kind:            "guild.launch.enqueue_requested",
		Severity:        infraevents.SeverityInfo,
		GuildID:         spec.ID,
		OrganizationID:  orgID,
		AgentID:         spawnReq.AgentSpec.ID,
		RequestID:       spawnReq.RequestID,
		SourceComponent: "forge-go.guild-bootstrap",
		Message:         "guild manager spawn enqueue requested",
	})

	if err := protocol.PushSpawnRequest(ctx, pusher, spawnReq); err != nil {
		_ = infraPublisher.Emit(ctx, infraevents.EmitParams{
			Kind:            "guild.launch.enqueue_failed",
			Severity:        infraevents.SeverityError,
			GuildID:         spec.ID,
			OrganizationID:  orgID,
			AgentID:         spawnReq.AgentSpec.ID,
			RequestID:       spawnReq.RequestID,
			SourceComponent: "forge-go.guild-bootstrap",
			Message:         "guild manager spawn enqueue failed",
			Detail: map[string]any{
				"error": err.Error(),
			},
		})
		return err
	}

	_ = infraPublisher.Emit(ctx, infraevents.EmitParams{
		Kind:            "guild.launch.enqueued",
		Severity:        infraevents.SeverityInfo,
		GuildID:         spec.ID,
		OrganizationID:  orgID,
		AgentID:         spawnReq.AgentSpec.ID,
		RequestID:       spawnReq.RequestID,
		SourceComponent: "forge-go.guild-bootstrap",
		Message:         "guild manager spawn enqueued",
	})

	return nil
}

func normalizeRuntimeSpecIDs(spec *protocol.GuildSpec, guildID string) {
	if spec.ID == "" {
		spec.ID = guildID
	}
	for i := range spec.Agents {
		defaultID := fmt.Sprintf("a-%d", i)
		if spec.Agents[i].ID == "" || spec.Agents[i].ID == defaultID {
			spec.Agents[i].ID = fmt.Sprintf("%s#a-%d", guildID, i)
		}
	}
}

func normalizeAgentModelIDs(agentModels []store.AgentModel, guildID string) {
	for i := range agentModels {
		defaultID := fmt.Sprintf("a-%d", i)
		if agentModels[i].ID == "" || agentModels[i].ID == defaultID {
			agentModels[i].ID = fmt.Sprintf("%s#a-%d", guildID, i)
		}
	}
}

func applyDefaults(spec *protocol.GuildSpec) {
	if spec.Properties == nil {
		spec.Properties = make(map[string]interface{})
	}
	for i := range spec.Agents {
		if spec.Agents[i].ID == "" {
			if spec.ID != "" {
				spec.Agents[i].ID = fmt.Sprintf("%s#a-%d", spec.ID, i)
			} else {
				spec.Agents[i].ID = fmt.Sprintf("a-%d", i)
			}
		}
		if spec.Agents[i].Properties == nil {
			spec.Agents[i].Properties = map[string]interface{}{}
		}
		if spec.Agents[i].AdditionalTopics == nil {
			spec.Agents[i].AdditionalTopics = []string{}
		}
		if spec.Agents[i].AdditionalDependencies == nil {
			spec.Agents[i].AdditionalDependencies = []string{}
		}
		if spec.Agents[i].DependencyMap == nil {
			spec.Agents[i].DependencyMap = map[string]protocol.DependencySpec{}
		}
		if spec.Agents[i].Predicates == nil {
			spec.Agents[i].Predicates = map[string]protocol.RuntimePredicate{}
		}
	}

	// Execution engine default (env var override supported)
	if spec.Properties["execution_engine"] == nil {
		ee := os.Getenv("RUSTIC_AI_EXECUTION_ENGINE")
		if ee == "" {
			ee = "rustic_ai.forge.execution_engine.ForgeExecutionEngine"
		}
		spec.Properties["execution_engine"] = ee
	}

	// Messaging default (env var overrides supported)
	if spec.Properties["messaging"] == nil {
		backendModule := os.Getenv("RUSTIC_AI_MESSAGING_MODULE")
		if backendModule == "" {
			backendModule = "rustic_ai.redis.messaging.backend"
		}
		backendClass := os.Getenv("RUSTIC_AI_MESSAGING_CLASS")
		if backendClass == "" {
			backendClass = "RedisMessagingBackend"
		}
		var backendConfig map[string]interface{}
		if raw := os.Getenv("RUSTIC_AI_MESSAGING_BACKEND_CONFIG"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &backendConfig)
		}
		if backendConfig == nil {
			if backendClass == "NATSMessagingBackend" {
				backendConfig = map[string]interface{}{}
			} else {
				redisHost := os.Getenv("REDIS_HOST")
				if redisHost == "" {
					redisHost = "localhost"
				}
				redisPort := os.Getenv("REDIS_PORT")
				if redisPort == "" {
					redisPort = "6379"
				}
				backendConfig = map[string]interface{}{
					"redis_client": map[string]interface{}{
						"host": redisHost,
						"port": redisPort,
						"db":   0,
					},
				}
			}
		}
		spec.Properties["messaging"] = map[string]interface{}{
			"backend_module": backendModule,
			"backend_class":  backendClass,
			"backend_config": backendConfig,
		}
	}

	// State manager default (env var override supported)
	if spec.Properties["state_manager"] == nil {
		if sm := os.Getenv("RUSTIC_AI_STATE_MANAGER"); sm != "" {
			spec.Properties["state_manager"] = sm
		}
	}
}

func applyStateManagerConfig(spec *protocol.GuildSpec, orgID, guildID string) {
	sm, _ := spec.Properties["state_manager"].(string)
	if !strings.Contains(sm, "DiskCacheStateManager") {
		return
	}
	if spec.Properties["state_manager_config"] != nil {
		return
	}
	cacheDir := filepath.Join(forgepath.ForgeHome(), "state_stores", orgID, guildID)
	spec.Properties["state_manager_config"] = map[string]interface{}{
		"cache_dir": cacheDir,
	}
}

func mergeDependencies(spec *protocol.GuildSpec, configPath string) error {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dependency config: %w", err)
	}

	var fileDeps map[string]protocol.DependencySpec
	if err := yaml.Unmarshal(fileData, &fileDeps); err != nil {
		return fmt.Errorf("parse dependency config: %w", err)
	}

	if spec.DependencyMap == nil {
		spec.DependencyMap = make(map[string]protocol.DependencySpec)
	}

	for key, fileDef := range fileDeps {
		if _, exists := spec.DependencyMap[key]; !exists {
			spec.DependencyMap[key] = fileDef
		}
	}

	return nil
}

func mergeRequiredDependencies(spec *protocol.GuildSpec, db store.Store, configPaths ...string) error {
	candidates := map[string]protocol.DependencySpec{}
	// Later paths have lower priority.
	for index := len(configPaths) - 1; index >= 0; index-- {
		data, err := os.ReadFile(configPaths[index])
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		parsed := map[string]protocol.DependencySpec{}
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			return err
		}
		for key, dependency := range parsed {
			candidates[key] = dependency
		}
	}

	// Conventional default keys remain the compatibility fallback for specs
	// created before agent dependency metadata was authoritative.
	required := map[string]bool{"filesystem": true, "llm": true}
	for _, agent := range spec.Agents {
		for _, key := range agent.AdditionalDependencies {
			required[strings.SplitN(key, ":", 2)[0]] = true
		}
		entry, err := db.GetAgentByClassName(agent.ClassName)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf(
					"agent %q uses unregistered class %q; cannot determine required dependencies",
					agent.ID,
					agent.ClassName,
				)
			}
			return fmt.Errorf(
				"look up dependency metadata for agent %q class %q: %w",
				agent.ID,
				agent.ClassName,
				err,
			)
		}
		for key := range entry.AgentDependencies {
			if _, explicitlyBound := agent.DependencyMap[key]; !explicitlyBound {
				required[key] = true
			}
		}
	}
	if spec.DependencyMap == nil {
		spec.DependencyMap = map[string]protocol.DependencySpec{}
	}
	for key := range required {
		if _, exists := spec.DependencyMap[key]; exists {
			continue
		}
		if dependency, exists := candidates[key]; exists {
			spec.DependencyMap[key] = dependency
		}
	}
	return nil
}

func stripDependencyCatalogMetadata(spec *protocol.GuildSpec) {
	for key, dependency := range spec.DependencyMap {
		dependency.ProvidedType = ""
		spec.DependencyMap[key] = dependency
	}
	for agentIndex := range spec.Agents {
		for key, dependency := range spec.Agents[agentIndex].DependencyMap {
			dependency.ProvidedType = ""
			spec.Agents[agentIndex].DependencyMap[key] = dependency
		}
	}
}

func buildModels(spec *protocol.GuildSpec, orgID, createdBy string) (*store.GuildModel, []store.AgentModel) {
	execEngine := "rustic_ai.core.guild.execution.sync.sync_exec_engine.SyncExecutionEngine"
	if custom, ok := spec.Properties["execution_engine"].(string); ok {
		execEngine = custom
	}

	guildID := os.Getenv("FORGE_STATIC_GUILD_ID")
	if guildID == "" {
		guildID = spec.ID
	}
	if guildID == "" {
		guildID = idgen.NewShortUUID()
	}

	gm := &store.GuildModel{
		ID:              guildID,
		Name:            spec.Name,
		Description:     spec.Description,
		ExecutionEngine: execEngine,
		OrganizationID:  orgID,
		CreatedBy:       createdBy,
		Properties:      store.JSONB(spec.Properties),
		BackendConfig:   store.JSONB{},
		DependencyMap:   dependencySpecsToJSONB(spec.DependencyMap),
		Status:          store.GuildStatusRequested,
	}

	if spec.Routes != nil {
		for _, rSpec := range spec.Routes.Steps {
			rModel := store.FromRoutingRule(gm.ID, &rSpec)
			gm.Routes = append(gm.Routes, *rModel)
		}
	}

	if msgConfigMap, ok := spec.Properties["messaging"].(map[string]interface{}); ok {
		if m, ok := msgConfigMap["backend_module"].(string); ok {
			gm.BackendModule = m
		}
		if c, ok := msgConfigMap["backend_class"].(string); ok {
			gm.BackendClass = c
		}
		if bc, ok := msgConfigMap["backend_config"].(map[string]interface{}); ok && bc != nil {
			gm.BackendConfig = store.JSONB(bc)
		}
	}

	var am []store.AgentModel
	for i, aSpec := range spec.Agents {
		if aSpec.ID == "" {
			aSpec.ID = fmt.Sprintf("%s#a-%d", gm.ID, i)
		}
		am = append(am, *store.FromAgentSpec(&aSpec, gm.ID))
	}

	return gm, am
}

func dependencySpecsToJSONB(specs map[string]protocol.DependencySpec) store.JSONB {
	out := store.JSONB{}
	for k, v := range specs {
		v.Normalize()
		out[k] = map[string]interface{}{
			"class_name": v.ClassName,
			"properties": v.Properties,
		}
	}
	return out
}
