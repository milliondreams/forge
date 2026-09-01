package api

import (
	"bytes"
	"fmt"
	"os"
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

type configuredDependencyProfile struct {
	ClassName    string                          `yaml:"class_name"`
	ProvidedType string                          `yaml:"provided_type"`
	Properties   map[string]interface{}          `yaml:"properties"`
	Catalog      dependencyCatalogMetadata       `yaml:"catalog"`
	Requirements protocol.CredentialRequirements `yaml:"requirements,omitempty"`
}

func (p configuredDependencyProfile) selectable() bool {
	return p.Catalog.Selectable == nil || *p.Catalog.Selectable
}

func (p configuredDependencyProfile) runtimeSpec() protocol.DependencySpec {
	return protocol.DependencySpec{ClassName: p.ClassName, ProvidedType: p.ProvidedType, Properties: p.Properties}
}

func (p configuredDependencyProfile) availability(key string) DependencyAvailability {
	if scheduler.GlobalNodeRegistry.AnyHealthyNodeReadyFor([]string{key}) || len(scheduler.GlobalNodeRegistry.ListHealthy()) == 0 {
		return DependencyAvailability{Status: "ready", Reasons: []string{}}
	}
	return DependencyAvailability{Status: "unavailable", Reasons: []string{"profile is not configured on any eligible node"}}
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&profiles); err != nil {
		return nil, fmt.Errorf("parse dependency config: %w", err)
	}
	for key, profile := range profiles {
		profile.ClassName = strings.TrimSpace(profile.ClassName)
		profile.ProvidedType = strings.TrimSpace(profile.ProvidedType)
		if profile.Properties == nil {
			profile.Properties = map[string]interface{}{}
		}
		profile.Requirements.Normalize()
		if err := profile.Requirements.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q requirements: %w", key, err)
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
func ReadyDependencyProfileKeys(configPath string, _ func(string) (bool, error)) ([]string, error) {
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return nil, err
	}
	ready := make([]string, 0, len(profiles))
	for key, profile := range profiles {
		if profile.selectable() {
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
