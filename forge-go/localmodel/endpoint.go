package localmodel

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

const BaseURLEnv = "AGENTOS_LOCAL_MODEL_BASE_URL"

// EndpointOverride returns the configured host-local model endpoint. An unset
// value is intentionally a no-op so normal host deployments retain their
// dependency configuration.
func EndpointOverride() (string, bool, error) {
	value := strings.TrimSpace(os.Getenv(BaseURLEnv))
	if value == "" {
		return "", false, nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", true, fmt.Errorf("%s must be an absolute HTTP(S) URL", BaseURLEnv)
	}
	if parsed.User != nil {
		return "", true, fmt.Errorf("%s must not contain credentials", BaseURLEnv)
	}

	return strings.TrimRight(value, "/"), true, nil
}

// ApplyDependencyOverride rewrites only Rustic local-model resolvers, leaving
// cloud providers and unrelated dependency definitions unchanged.
func ApplyDependencyOverride(dependencies map[string]protocol.DependencySpec) error {
	endpoint, configured, err := EndpointOverride()
	if err != nil || !configured {
		return err
	}

	for key, spec := range dependencies {
		if !isLocalModelDependency(key, spec) {
			continue
		}

		properties := cloneMap(spec.Properties)
		conf, _ := properties["conf"].(map[string]any)
		conf = cloneMap(conf)
		conf["base_url"] = endpoint
		properties["conf"] = conf
		delete(properties, "base_url")
		spec.Properties = properties
		dependencies[key] = spec
	}
	return nil
}

func isLocalModelDependency(key string, spec protocol.DependencySpec) bool {
	if key == "llm" || strings.HasPrefix(key, "llm_local_") {
		model, _ := spec.Properties["model"].(string)
		return strings.HasPrefix(strings.TrimSpace(model), "openai/rustic/")
	}
	return false
}

func cloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
