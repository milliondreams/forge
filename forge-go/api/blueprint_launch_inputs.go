package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const dependencyAnnotationKey = "x-rustic-dependency"

const (
	dependencyValueShapeKey         = "key"
	dependencyValueShapePublicEntry = "public_entry"
)

type blueprintDependencyAnnotation struct {
	Selection    string                    `json:"selection"`
	ValueShape   string                    `json:"value_shape"`
	RequiredType string                    `json:"required_type"`
	Filters      blueprintDependencyFilter `json:"filters"`
	Target       blueprintDependencyTarget `json:"target"`
}

type blueprintDependencyFilter struct {
	Capabilities []string `json:"capabilities"`
	Profiles     []string `json:"profiles"`
}

type blueprintDependencyTarget struct {
	Kind          string `json:"kind"`
	AgentID       string `json:"agent_id"`
	DependencyKey string `json:"dependency_key"`
	CatalogKey    string `json:"catalog_key"`
}

func validateBlueprintDependencyAnnotations(s store.Store, bp *store.Blueprint) error {
	rawSchema, ok := bp.Spec["configuration_schema"].(map[string]interface{})
	if !ok {
		return nil
	}
	properties, _ := rawSchema["properties"].(map[string]interface{})

	for fieldName, rawProperty := range properties {
		property, ok := rawProperty.(map[string]interface{})
		if !ok {
			continue
		}
		rawAnnotation, annotated := property[dependencyAnnotationKey]
		if !annotated {
			continue
		}
		annotation, err := decodeDependencyAnnotation(rawAnnotation)
		if err != nil {
			return fmt.Errorf("configuration field %q: %w", fieldName, err)
		}
		if err := validateDependencyAnnotation(s, bp, fieldName, property, annotation); err != nil {
			return err
		}
	}
	return nil
}

func materializeBlueprintDependencySelections(
	s store.Store,
	bp *store.Blueprint,
	spec *protocol.GuildSpec,
	configuration map[string]interface{},
	legacyBindings map[string]string,
	configPath string,
) error {
	schema, _ := bp.Spec["configuration_schema"].(map[string]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return err
	}
	if spec.Properties == nil {
		spec.Properties = map[string]interface{}{}
	}
	if spec.DependencyMap == nil {
		spec.DependencyMap = map[string]protocol.DependencySpec{}
	}
	snapshots := map[string]interface{}{}

	for fieldName, rawProperty := range properties {
		property, _ := rawProperty.(map[string]interface{})
		rawAnnotation, ok := property[dependencyAnnotationKey]
		if !ok {
			continue
		}
		annotation, err := decodeDependencyAnnotation(rawAnnotation)
		if err != nil {
			return fmt.Errorf("configuration field %q: %w", fieldName, err)
		}
		if err := validateDependencyAnnotation(s, bp, fieldName, property, annotation); err != nil {
			return err
		}
		value, present := configuration[fieldName]
		if !present {
			if configurationFieldRequired(schema, fieldName) {
				return fmt.Errorf("configuration field %q is required", fieldName)
			}
			continue
		}
		selected, err := selectedProfileKeys(value, annotation.Selection, annotation.ValueShape)
		if err != nil {
			return fmt.Errorf("configuration field %q: %w", fieldName, err)
		}
		if annotation.Target.Kind == "agent_dependency" && len(selected) != 1 {
			return fmt.Errorf("configuration field %q must select exactly one profile", fieldName)
		}

		profileMetadata := map[string]interface{}{}
		for index, key := range selected {
			profile, exists := profiles[key]
			if !exists || !profileMatchesAnnotation(key, profile, annotation) {
				return fmt.Errorf("profile %q is not allowed for configuration field %q", key, fieldName)
			}
			availability := profile.availability()
			if availability.Status != "ready" {
				return fmt.Errorf("profile %q is unavailable: %s", key, strings.Join(availability.Reasons, ", "))
			}
			runtimeSpec := profile.runtimeSpec()
			if annotation.Target.Kind == "agent_dependency" {
				agent := findAgent(spec, annotation.Target.AgentID)
				if agent == nil {
					return fmt.Errorf("target agent %q not found", annotation.Target.AgentID)
				}
				if existing, exists := agent.DependencyMap[annotation.Target.DependencyKey]; exists && !reflect.DeepEqual(existing, runtimeSpec) {
					return fmt.Errorf("dependency selection conflicts with existing binding for agent %q dependency %q", annotation.Target.AgentID, annotation.Target.DependencyKey)
				}
				agent.DependencyMap[annotation.Target.DependencyKey] = runtimeSpec
				bindingKey := "agent:" + annotation.Target.AgentID + ":" + annotation.Target.DependencyKey
				if legacy, exists := legacyBindings[bindingKey]; exists && legacy != key {
					return fmt.Errorf("dependency_bindings conflicts with configuration field %q", fieldName)
				}
			} else {
				internalKey := fmt.Sprintf("__selection_%s_%d", fieldName, index)
				spec.DependencyMap[internalKey] = runtimeSpec
				entry := profile.publicEntry(key)
				profileMetadata[key] = map[string]interface{}{
					"dependency_key": internalKey,
					"display_name":   entry.DisplayName,
					"aliases":        entry.Aliases,
				}
			}
		}
		if annotation.Target.Kind == "runtime_catalog" {
			snapshots[annotation.Target.CatalogKey] = map[string]interface{}{
				"required_type":  annotation.RequiredType,
				"dependency_key": annotation.Target.DependencyKey,
				"profiles":       profileMetadata,
			}
		}
	}
	if len(snapshots) > 0 {
		spec.Properties["dependency_selections"] = snapshots
	}
	return nil
}

func configurationFieldRequired(schema map[string]interface{}, fieldName string) bool {
	switch required := schema["required"].(type) {
	case []interface{}:
		for _, value := range required {
			if value == fieldName {
				return true
			}
		}
	case []string:
		for _, value := range required {
			if value == fieldName {
				return true
			}
		}
	}
	return false
}

func validateDependencyAnnotation(s store.Store, bp *store.Blueprint, fieldName string, property map[string]interface{}, annotation blueprintDependencyAnnotation) error {
	if annotation.RequiredType == "" || annotation.Target.DependencyKey == "" {
		return fmt.Errorf("configuration field %q has incomplete dependency annotation", fieldName)
	}
	if annotation.Selection == "single" {
		if property["type"] != "string" {
			return fmt.Errorf("configuration field %q single selection must be a string", fieldName)
		}
	} else if annotation.Selection == "multiple" {
		if property["type"] != "array" || property["uniqueItems"] != true {
			return fmt.Errorf("configuration field %q multiple selection must be an array with uniqueItems", fieldName)
		}
	} else {
		return fmt.Errorf("configuration field %q has unsupported selection %q", fieldName, annotation.Selection)
	}
	valueShape := annotation.ValueShape
	if valueShape == "" {
		valueShape = dependencyValueShapeKey
	}
	if valueShape != dependencyValueShapeKey && valueShape != dependencyValueShapePublicEntry {
		return fmt.Errorf("configuration field %q has unsupported value_shape %q", fieldName, valueShape)
	}
	if valueShape == dependencyValueShapePublicEntry && (annotation.Selection != "multiple" || annotation.Target.Kind != "runtime_catalog") {
		return fmt.Errorf("configuration field %q public_entry values require a multiple runtime catalog selection", fieldName)
	}
	if annotation.Target.Kind == "runtime_catalog" {
		if annotation.Target.CatalogKey == "" {
			return fmt.Errorf("configuration field %q runtime catalog key is required", fieldName)
		}
		return nil
	}
	if annotation.Target.Kind != "agent_dependency" || annotation.Target.AgentID == "" || strings.Contains(annotation.Target.AgentID, "{{") {
		return fmt.Errorf("configuration field %q has invalid agent dependency target", fieldName)
	}
	rawAgents, ok := bp.Spec["agents"].([]interface{})
	if !ok {
		return fmt.Errorf("configuration field %q cannot resolve agents from the blueprint", fieldName)
	}
	className := ""
	for _, rawAgent := range rawAgents {
		agent, _ := rawAgent.(map[string]interface{})
		if agent["id"] == annotation.Target.AgentID {
			className, _ = agent["class_name"].(string)
			break
		}
	}
	entry, err := s.GetAgentByClassName(className)
	if err != nil {
		return fmt.Errorf("configuration field %q target agent is not registered", fieldName)
	}
	for _, dependency := range catalogAgentToResponse(entry).AgentDependencies {
		requiredType := dependency.RequiredType
		if requiredType == nil {
			requiredType = dependency.ResolvedType
		}
		if dependency.DependencyKey == annotation.Target.DependencyKey && requiredType != nil && *requiredType == annotation.RequiredType {
			return nil
		}
	}
	return fmt.Errorf("configuration field %q dependency type does not match registered agent metadata", fieldName)
}

func decodeDependencyAnnotation(value interface{}) (blueprintDependencyAnnotation, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return blueprintDependencyAnnotation{}, err
	}
	var annotation blueprintDependencyAnnotation
	if err := json.Unmarshal(data, &annotation); err != nil {
		return blueprintDependencyAnnotation{}, err
	}
	return annotation, nil
}

func profileMatchesAnnotation(key string, profile configuredDependencyProfile, annotation blueprintDependencyAnnotation) bool {
	if !profile.selectable() || profile.ProvidedType != annotation.RequiredType {
		return false
	}
	if len(annotation.Filters.Profiles) > 0 && !containsFold(annotation.Filters.Profiles, key) {
		return false
	}
	for _, capability := range annotation.Filters.Capabilities {
		if !containsFold(profile.Catalog.Capabilities, capability) {
			return false
		}
	}
	return true
}

func selectedProfileKeys(value interface{}, selection, valueShape string) ([]string, error) {
	if valueShape == "" {
		valueShape = dependencyValueShapeKey
	}
	if selection == "single" {
		selected, ok := value.(string)
		if !ok || strings.TrimSpace(selected) == "" {
			return nil, fmt.Errorf("profile selection is required")
		}
		return []string{selected}, nil
	}
	values, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("profile selections must be an array")
	}
	seen := map[string]bool{}
	selected := make([]string, 0, len(values))
	for _, value := range values {
		var key string
		switch typed := value.(type) {
		case string:
			key = strings.TrimSpace(typed)
		case map[string]interface{}:
			if valueShape != dependencyValueShapePublicEntry {
				return nil, fmt.Errorf("profile selections must contain unique strings")
			}
			key, _ = typed["key"].(string)
			key = strings.TrimSpace(key)
		default:
			return nil, fmt.Errorf("profile selections must contain strings or public dependency entries")
		}
		if key == "" {
			return nil, fmt.Errorf("profile selections must contain a non-empty key")
		}
		if seen[key] {
			return nil, fmt.Errorf("profile selections must contain unique keys")
		}
		seen[key] = true
		selected = append(selected, key)
	}
	return selected, nil
}

func findAgent(spec *protocol.GuildSpec, id string) *protocol.AgentSpec {
	for index := range spec.Agents {
		if spec.Agents[index].ID == id {
			if spec.Agents[index].DependencyMap == nil {
				spec.Agents[index].DependencyMap = map[string]protocol.DependencySpec{}
			}
			return &spec.Agents[index]
		}
	}
	return nil
}
