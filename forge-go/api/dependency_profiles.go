package api

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/protocol"
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
	return protocol.DependencySpec{ClassName: p.ClassName, Properties: p.Properties}
}

func (p configuredDependencyProfile) availability() DependencyAvailability {
	reasons := make([]string, 0)
	for _, secret := range p.Requirements.Secrets {
		if strings.TrimSpace(os.Getenv(secret)) == "" {
			reasons = append(reasons, "missing "+secret)
		}
	}
	if len(reasons) > 0 {
		return DependencyAvailability{Status: "needs_configuration", Reasons: reasons}
	}
	return DependencyAvailability{Status: "ready", Reasons: []string{}}
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
		Availability: p.availability(),
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
		if !includeUnavailable && profile.availability().Status != "ready" {
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

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
