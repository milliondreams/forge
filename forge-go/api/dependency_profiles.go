package api

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"gopkg.in/yaml.v3"
)

type dependencyCatalogMetadata struct {
	DisplayName  string   `yaml:"display_name"`
	Description  string   `yaml:"description"`
	Provider     string   `yaml:"provider"`
	Capabilities []string `yaml:"capabilities"`
	Aliases      []string `yaml:"aliases"`
	Selectable   *bool    `yaml:"selectable"`
}

type dependencyRequirements struct {
	Secrets []string `yaml:"secrets"`
}

type configuredDependencyProfile struct {
	ClassName    string                    `yaml:"class_name"`
	ProvidedType string                    `yaml:"provided_type"`
	Properties   map[string]interface{}    `yaml:"properties"`
	Catalog      dependencyCatalogMetadata `yaml:"catalog"`
	Requirements dependencyRequirements    `yaml:"requirements"`
}

func (p configuredDependencyProfile) selectable() bool {
	return p.Catalog.Selectable == nil || *p.Catalog.Selectable
}

func (p configuredDependencyProfile) runtimeSpec() protocol.DependencySpec {
	return protocol.DependencySpec{ClassName: p.ClassName, ProvidedType: p.ProvidedType, Properties: p.Properties}
}

func appendUniqueSecrets(resources *protocol.ResourceSpec, names ...string) {
	seen := make(map[string]struct{}, len(resources.Secrets)+len(names))
	result := make([]string, 0, len(resources.Secrets)+len(names))
	for _, name := range append(append([]string{}, resources.Secrets...), names...) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	resources.Secrets = result
}

// enrichAgentDependencyRequirements adds requirements only when a dependency
// exactly and uniquely matches a configured profile. This preserves legacy
// explicit dependency specs without guessing between similar profiles.
func enrichAgentDependencyRequirements(agent *protocol.AgentSpec, profiles map[string]configuredDependencyProfile) {
	profileKeys := make([]string, 0)
	for _, dependency := range agent.DependencyMap {
		type profileMatch struct {
			key     string
			profile configuredDependencyProfile
		}
		matches := make([]profileMatch, 0, 1)
		for key, profile := range profiles {
			if dependencyMatchesProfile(dependency, profile) {
				matches = append(matches, profileMatch{key: key, profile: profile})
			}
		}
		if len(matches) == 1 {
			appendUniqueSecrets(&agent.Resources, matches[0].profile.Requirements.Secrets...)
			profileKeys = append(profileKeys, matches[0].key)
		}
	}
	if len(profileKeys) > 0 {
		sort.Strings(profileKeys)
		agent.Properties[protocol.DependencyProfilesProperty] = profileKeys
	}
}

func dependencyMatchesProfile(dependency protocol.DependencySpec, profile configuredDependencyProfile) bool {
	runtimeSpec := profile.runtimeSpec()
	if dependency.ClassName != runtimeSpec.ClassName || !reflect.DeepEqual(dependency.Properties, runtimeSpec.Properties) {
		return false
	}
	return dependency.ProvidedType == "" || dependency.ProvidedType == runtimeSpec.ProvidedType
}

func (p configuredDependencyProfile) availability(key string) DependencyAvailability {
	if len(p.Requirements.Secrets) == 0 || scheduler.GlobalNodeRegistry.AnyHealthyNodeReadyFor([]string{key}) {
		return DependencyAvailability{Status: "ready", Reasons: []string{}}
	}
	return DependencyAvailability{Status: "needs_configuration", Reasons: []string{"not ready on any eligible node"}}
}

func (p configuredDependencyProfile) publicEntry(key string) ConfiguredDependencyEntry {
	displayName := strings.TrimSpace(p.Catalog.DisplayName)
	if displayName == "" {
		displayName = key
	}
	return ConfiguredDependencyEntry{
		Key:          key,
		DisplayName:  displayName,
		Description:  p.Catalog.Description,
		ProvidedType: p.ProvidedType,
		Provider:     p.Catalog.Provider,
		Capabilities: append([]string{}, p.Catalog.Capabilities...),
		Aliases:      append([]string{}, p.Catalog.Aliases...),
		Availability: p.availability(key),
	}
}

func loadConfiguredDependencyProfiles(configPath string) (map[string]configuredDependencyProfile, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]configuredDependencyProfile{}, nil
		}
		return nil, fmt.Errorf("read dependency config: %w", err)
	}
	profiles := map[string]configuredDependencyProfile{}
	if err := yaml.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("parse dependency config: %w", err)
	}
	for key, profile := range profiles {
		profile.ClassName = strings.TrimSpace(profile.ClassName)
		profile.ProvidedType = strings.TrimSpace(profile.ProvidedType)
		if profile.Properties == nil {
			profile.Properties = map[string]interface{}{}
		}
		profiles[key] = profile
	}
	return profiles, nil
}

func safeConfiguredDependencyEntries(configPath, providedType, capability string, includeUnavailable bool) ([]ConfiguredDependencyEntry, error) {
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(profiles))
	for key, profile := range profiles {
		if !profile.selectable() || (providedType != "" && profile.ProvidedType != providedType) {
			continue
		}
		if !includeUnavailable && profile.availability(key).Status != "ready" {
			continue
		}
		if capability != "" && !containsFold(profile.Catalog.Capabilities, capability) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ConfiguredDependencyEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, profiles[key].publicEntry(key))
	}
	return out, nil
}

// ReadyDependencyProfileKeys evaluates a node's configured profiles without
// exposing requirement names in node registration payloads.
func ReadyDependencyProfileKeys(configPath string, secretExists func(string) (bool, error)) ([]string, error) {
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return nil, err
	}
	ready := make([]string, 0, len(profiles))
	for key, profile := range profiles {
		isReady := true
		for _, name := range profile.Requirements.Secrets {
			exists, err := secretExists(name)
			if err != nil {
				return nil, fmt.Errorf("check requirement for profile %q: %w", key, err)
			}
			if !exists {
				isReady = false
				break
			}
		}
		if isReady {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	return ready, nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
