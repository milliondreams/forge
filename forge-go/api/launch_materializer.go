package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// materializeBlueprintLaunch is the only interpretation of a launch request.
// Preflight and launch both use it so the checked plan is the persisted plan.
func materializeBlueprintLaunch(
	s store.Store,
	blueprint *store.Blueprint,
	req LaunchGuildFromBlueprintRequest,
) (*protocol.GuildSpec, error) {
	specMap := map[string]interface{}{}
	data, err := json.Marshal(blueprint.Spec)
	if err != nil {
		return nil, fmt.Errorf("marshal blueprint spec: %w", err)
	}
	if err := json.Unmarshal(data, &specMap); err != nil {
		return nil, fmt.Errorf("copy blueprint spec: %w", err)
	}
	assignStableBlueprintAgentIDs(blueprint, specMap)

	if rawSchema, exists := specMap["configuration_schema"]; exists {
		schema, ok := rawSchema.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("configuration_schema must be object")
		}
		merged := map[string]interface{}{}
		if base, ok := specMap["configuration"].(map[string]interface{}); ok {
			for key, value := range base {
				merged[key] = value
			}
		}
		for key, value := range req.Configuration {
			merged[key] = value
		}
		if err := validateAgainstSchema(schema, merged); err != nil {
			return nil, fmt.Errorf("configuration and/or schema invalid: %w", err)
		}
		specMap["configuration"] = merged
	}
	delete(specMap, "configuration_schema")

	specBytes, err := json.Marshal(specMap)
	if err != nil {
		return nil, fmt.Errorf("marshal materialized guild spec: %w", err)
	}
	var guildSpec protocol.GuildSpec
	if err := json.Unmarshal(specBytes, &guildSpec); err != nil {
		return nil, fmt.Errorf("invalid guild spec: %w", err)
	}
	rendered, err := guild.RenderConfiguration(&guildSpec)
	if err != nil {
		return nil, fmt.Errorf("invalid guild spec: %w", err)
	}
	guildSpec = *rendered
	if req.GuildID != nil {
		guildSpec.ID = *req.GuildID
	}
	if guildSpec.ID == "" {
		guildSpec.ID = idgen.NewShortUUID()
	}
	guildSpec.Name = req.GuildName
	if req.Description != nil {
		guildSpec.Description = *req.Description
	}
	if err := materializeBlueprintDependencySelections(
		s, blueprint, &guildSpec, guildSpec.Configuration, dependencyConfigPath(),
	); err != nil {
		return nil, fmt.Errorf("invalid dependency selection: %w", err)
	}
	guildSpec.Normalize()
	return &guildSpec, nil
}

func assignStableBlueprintAgentIDs(blueprint *store.Blueprint, spec map[string]interface{}) {
	rawAgents, ok := spec["agents"].([]interface{})
	if !ok {
		return
	}
	blueprintIdentity := blueprint.ID
	if blueprintIdentity == "" {
		blueprintIdentity = blueprint.Name + "\x00" + blueprint.Version
	}
	for index, rawAgent := range rawAgents {
		agent, ok := rawAgent.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := agent["id"].(string); ok && id != "" {
			continue
		}
		identityAgent := make(map[string]interface{}, len(agent))
		for key, value := range agent {
			if key != "id" {
				identityAgent[key] = value
			}
		}
		canonicalAgent, _ := json.Marshal(identityAgent)
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", blueprintIdentity, index, canonicalAgent)))
		agent["id"] = "bpagent-" + hex.EncodeToString(sum[:12])
	}
}
