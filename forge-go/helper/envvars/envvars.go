package envvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

// MissingCredentialError identifies a mandatory credential that was not
// available when Forge prepared an agent process environment.
type MissingCredentialError struct {
	Kind string
	Name string
}

func (e *MissingCredentialError) Error() string {
	return fmt.Sprintf("mandatory %s credential %q is not configured", e.Kind, e.Name)
}

// BuildAgentEnv constructs the allowlisted environment for a Forge agent
// process. Only Forge runtime settings and credentials resolved from the
// authoritative requirement set are included.
func BuildAgentEnv(
	ctx context.Context,
	guildSpec *protocol.GuildSpec,
	agentSpec *protocol.AgentSpec,
	requirements protocol.CredentialRequirements,
	secretProvider secrets.SecretProvider,
	orgID string,
) ([]string, error) {

	envMap := make(map[string]string)

	guildBytes, err := json.Marshal(guildSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal guild spec: %w", err)
	}
	envMap["FORGE_GUILD_JSON"] = string(guildBytes)

	agentBytes, err := json.Marshal(agentSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal agent spec: %w", err)
	}
	envMap["FORGE_AGENT_CONFIG_JSON"] = string(agentBytes)

	backendModule := "rustic_ai.redis.messaging.backend"
	backendClass := "RedisMessagingBackend"
	backendConfig := map[string]interface{}{}
	if guildSpec.Properties != nil {
		if msgMap, ok := guildSpec.Properties["messaging"].(map[string]interface{}); ok {
			if bm, ok := msgMap["backend_module"].(string); ok {
				backendModule = bm
			}
			if bc, ok := msgMap["backend_class"].(string); ok {
				backendClass = bc
			}
			if bcfg, ok := msgMap["backend_config"].(map[string]interface{}); ok && bcfg != nil {
				backendConfig = bcfg
			}
		}
	}

	// Preserve existing FORGE_CLIENT_PROPERTIES_JSON if no backend config was found in the guild spec
	if osProps := os.Getenv("FORGE_CLIENT_PROPERTIES_JSON"); osProps != "" && len(backendConfig) == 0 {
		var osConfig map[string]interface{}
		if err := json.Unmarshal([]byte(osProps), &osConfig); err == nil {
			backendConfig = osConfig
		}
	}

	// Inject redis_client defaults if using Redis and not already configured
	if backendClass == "RedisMessagingBackend" {
		if _, exists := backendConfig["redis_client"]; !exists {
			host := os.Getenv("REDIS_HOST")
			if host == "" {
				host = "localhost"
			}
			port := os.Getenv("REDIS_PORT")
			if port == "" {
				port = "6379"
			}
			backendConfig["redis_client"] = map[string]interface{}{
				"host": host,
				"port": port,
				"db":   0,
			}
		}
	}

	// Inject nats_client defaults if using NATS and not already configured.
	// Matches the Python auto-injection in agent_runner.py.
	if backendClass == "NATSMessagingBackend" {
		if _, exists := backendConfig["nats_client"]; !exists {
			natsURL := os.Getenv("NATS_URL")
			if natsURL == "" {
				natsURL = "nats://localhost:4222"
			}
			backendConfig["nats_client"] = map[string]interface{}{
				"servers": []string{natsURL},
			}
		}
	}

	envMap["FORGE_CLIENT_MODULE"] = backendModule
	envMap["FORGE_CLIENT_TYPE"] = backendClass
	if len(backendConfig) > 0 {
		backendBytes, err := json.Marshal(backendConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal backend config: %w", err)
		}
		envMap["FORGE_CLIENT_PROPERTIES_JSON"] = string(backendBytes)
	} else {
		envMap["FORGE_CLIENT_PROPERTIES_JSON"] = "{}"
	}

	if err := resolveSecrets(ctx, agentSpec, requirements, secretProvider, orgID, envMap); err != nil {
		return nil, err
	}

	uvCacheDir := os.Getenv("FORGE_UV_CACHE_DIR")
	if uvCacheDir == "" {
		uvCacheDir = forgepath.Resolve("uv_cache")
	}
	if uvCacheDir != "" {
		envMap["UV_CACHE_DIR"] = uvCacheDir
	}

	// Forward Redis connection env vars so spawned containers can find the correct Redis instance.
	// Without this, containers default to localhost:6379 instead of the intended Redis.
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB"} {
		if val := os.Getenv(key); val != "" {
			envMap[key] = val
		}
	}

	// Forward NATS URL so spawned agents can connect to the same NATS server.
	if val := os.Getenv("NATS_URL"); val != "" {
		envMap["NATS_URL"] = val
	}

	// Forward state manager and FORGE_HOME env vars so spawned agents inherit state store config.
	for _, key := range []string{"RUSTIC_AI_STATE_MANAGER", "FORGE_HOME"} {
		if val := os.Getenv(key); val != "" {
			envMap[key] = val
		}
	}

	var result []string
	for k, v := range envMap {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}

	return result, nil
}

func resolveSecrets(
	ctx context.Context,
	agentSpec *protocol.AgentSpec,
	requirements protocol.CredentialRequirements,
	secretProvider secrets.SecretProvider,
	orgID string,
	envMap map[string]string,
) error {
	requirements.Normalize()
	if err := requirements.Validate(); err != nil {
		return fmt.Errorf("invalid credential requirements for agent %q: %w", agentSpec.ID, err)
	}
	keys := make([]string, 0, len(requirements.Secrets))
	byKey := make(map[string]protocol.SecretNeed, len(requirements.Secrets))
	for _, requirement := range requirements.Secrets {
		keys = append(keys, requirement.Key)
		byKey[requirement.Key] = requirement
	}
	sort.Strings(keys)
	values, failures := resolveStaticSecretBatch(ctx, secretProvider, orgID, keys)
	for _, key := range keys {
		requirement := byKey[key]
		if err := failures[key]; err != nil {
			if errors.Is(err, secrets.ErrSecretNotFound) {
				if requirement.Optional != nil && *requirement.Optional {
					continue
				}
				return &MissingCredentialError{Kind: "secret", Name: key}
			}
			return fmt.Errorf("failed to resolve secret '%s' for agent '%s': %w", key, agentSpec.ID, err)
		}
		envMap[requirement.Env] = values[key]
	}

	for _, o := range requirements.OAuth {
		secretKey := oauth.StoreKey(orgID, o.Provider)
		val, err := secretProvider.Resolve(ctx, secretKey)
		if err != nil {
			if errors.Is(err, secrets.ErrSecretNotFound) {
				if o.Optional != nil && *o.Optional {
					continue
				}
				return &MissingCredentialError{Kind: "oauth", Name: o.Provider}
			}
			return fmt.Errorf("failed to resolve OAuth token for provider '%s', agent '%s': %w", o.Provider, agentSpec.ID, err)
		}
		envMap[o.Env] = val
	}

	return nil
}

type batchSecretProvider interface {
	ResolveBatch(context.Context, []string) (map[string]string, map[string]error)
}

func resolveStaticSecretBatch(ctx context.Context, provider secrets.SecretProvider, orgID string, keys []string) (map[string]string, map[string]error) {
	values := make(map[string]string, len(keys))
	failures := make(map[string]error)
	scopedToRaw := make(map[string]string, len(keys))
	scopedKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		scoped := secrets.SecretStoreKey(orgID, key)
		scopedKeys = append(scopedKeys, scoped)
		scopedToRaw[scoped] = key
	}

	batch, supportsBatch := provider.(batchSecretProvider)
	var scopedValues map[string]string
	var scopedFailures map[string]error
	if supportsBatch {
		scopedValues, scopedFailures = batch.ResolveBatch(ctx, scopedKeys)
	} else {
		scopedValues, scopedFailures = resolveIndividually(ctx, provider, scopedKeys)
	}
	for scoped, key := range scopedToRaw {
		if value, ok := scopedValues[scoped]; ok {
			values[key] = value
			continue
		}
		err := scopedFailures[scoped]
		if !errors.Is(err, secrets.ErrSecretNotFound) {
			failures[key] = err
			continue
		}
		failures[key] = secrets.ErrSecretNotFound
	}
	return values, failures
}

func resolveIndividually(ctx context.Context, provider secrets.SecretProvider, keys []string) (map[string]string, map[string]error) {
	values := make(map[string]string, len(keys))
	failures := make(map[string]error)
	for _, key := range keys {
		value, err := provider.Resolve(ctx, key)
		if err != nil {
			failures[key] = err
		} else {
			values[key] = value
		}
	}
	return values, failures
}
