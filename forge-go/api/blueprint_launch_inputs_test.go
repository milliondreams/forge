package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const testLLMType = "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM"

func TestDependencyAnnotationUsesCanonicalProfileKey(t *testing.T) {
	if dependencyAnnotationKey != "x-rustic-profile" {
		t.Fatalf("dependency annotation key = %q", dependencyAnnotationKey)
	}

	bp := &store.Blueprint{Spec: store.JSONB{
		"configuration_schema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"legacy": map[string]interface{}{
					"type": "string",
					"x-rustic-dependency": map[string]interface{}{
						"selection": "invalid",
					},
				},
			},
		},
	}}

	if err := validateBlueprintDependencyAnnotations(nil, bp); err != nil {
		t.Fatalf("legacy annotation should not be interpreted: %v", err)
	}
}

func TestMaterializeLocalNomicEmbeddingSelection(t *testing.T) {
	db, err := store.NewGormStore("sqlite", filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "example.EmbeddingAgent",
		AgentName:          "EmbeddingAgent",
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
		AgentDependencies: store.JSONB{
			"embeddings": map[string]interface{}{
				"dependency_key": "embeddings", "required_type": embeddingsType,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	annotation := map[string]interface{}{
		"selection": "single", "required_type": embeddingsType,
		"filters": map[string]interface{}{"capabilities": []interface{}{"embeddings"}},
		"target": map[string]interface{}{
			"kind": "agent_dependency", "agent_id": "embedder", "dependency_key": "embeddings",
		},
	}
	bp := &store.Blueprint{Spec: store.JSONB{
		"name": "Embedding Guild", "description": "test",
		"configuration_schema": map[string]interface{}{
			"type": "object", "required": []interface{}{"embedding_profile"},
			"properties": map[string]interface{}{
				"embedding_profile": map[string]interface{}{
					"type": "string", dependencyAnnotationKey: annotation,
				},
			},
		},
		"configuration": map[string]interface{}{"embedding_profile": "embeddings_local_nomic"},
		"agents": []interface{}{map[string]interface{}{
			"id": "embedder", "name": "Embedder", "description": "test", "class_name": "example.EmbeddingAgent",
		}},
	}}

	var spec protocol.GuildSpec
	data, _ := json.Marshal(bp.Spec)
	if err := spec.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]interface{}{"embedding_profile": "embeddings_local_nomic"}
	configPath := filepath.Join("..", "conf", "agent-dependencies.yaml")
	if err := materializeBlueprintDependencySelections(db, bp, &spec, configuration, nil, configPath); err != nil {
		t.Fatal(err)
	}

	resolved := spec.Agents[0].DependencyMap["embeddings"]
	if resolved.ClassName != "rustic_ai.langchain.agent_ext.embeddings.openai.OpenAIEmbeddingsResolver" {
		t.Fatalf("embedding resolver = %q", resolved.ClassName)
	}
	if resolved.Properties["model_name"] != "rustic/nomic-embed-default" {
		t.Fatalf("embedding properties = %#v", resolved.Properties)
	}
}

func TestBlueprintDependencyAnnotationsAndMaterialization(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	registerTestLLMAgent(t, db)

	configPath := filepath.Join(t.TempDir(), "dependencies.yaml")
	config := `
local:
  class_name: example.LLMResolver
  provided_type: ` + testLLMType + `
  catalog:
    display_name: Local Model
    provider: local
    capabilities: [chat]
    aliases: [local-model]
    selectable: true
  properties:
    model: local/model
cloud:
  class_name: example.LLMResolver
  provided_type: ` + testLLMType + `
  catalog:
    display_name: Cloud Model
    provider: cloud
    capabilities: [chat]
    aliases: [cloud-model]
    selectable: true
  requirements:
    secrets: [TEST_CLOUD_API_KEY]
  properties:
    model: cloud/model
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := safeConfiguredDependencyEntries(configPath, testLLMType, "chat", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Key != "local" {
		t.Fatalf("ready dependency catalog = %#v, want only local", entries)
	}
	allSelectable, err := safeConfiguredDependencyEntries(configPath, testLLMType, "chat", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(allSelectable) != 2 {
		t.Fatalf("catalog including unavailable = %#v, want two profiles", allSelectable)
	}
	encodedEntries, _ := json.Marshal(allSelectable)
	if bytes.Contains(encodedEntries, []byte("class_name")) || bytes.Contains(encodedEntries, []byte("properties")) {
		t.Fatalf("safe catalog leaked runtime resolver configuration: %s", encodedEntries)
	}

	bp, err := db.CreateBlueprint(testDependencyBlueprint())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBlueprintDependencyAnnotations(db, bp); err != nil {
		t.Fatalf("validate dependency annotations: %v", err)
	}

	var spec protocol.GuildSpec
	data, _ := json.Marshal(bp.Spec)
	if err := spec.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]interface{}{
		"model": "local",
		"dynamic_models": []interface{}{map[string]interface{}{
			"key":          "local",
			"display_name": "Spoofed Model",
			"aliases":      []interface{}{"spoofed"},
		}},
	}
	if err := materializeBlueprintDependencySelections(db, bp, &spec, configuration, nil, configPath); err != nil {
		t.Fatal(err)
	}
	if spec.Agents[0].DependencyMap["llm"].Properties["model"] != "local/model" {
		t.Fatalf("starter agent did not receive selected resolver: %#v", spec.Agents[0].DependencyMap)
	}
	if _, ok := spec.DependencyMap["__selection_dynamic_models_0"]; !ok {
		t.Fatalf("dynamic resolver snapshot missing: %#v", spec.DependencyMap)
	}
	if _, ok := spec.Properties["dependency_selections"]; !ok {
		t.Fatalf("safe dynamic selection metadata missing: %#v", spec.Properties)
	}
	catalogs := spec.Properties["dependency_selections"].(map[string]interface{})
	profiles := catalogs["dynamic_models"].(map[string]interface{})["profiles"].(map[string]interface{})
	metadata := profiles["local"].(map[string]interface{})
	if metadata["display_name"] != "Local Model" {
		t.Fatalf("client metadata replaced trusted catalog snapshot: %#v", metadata)
	}
}

func TestSelectedProfileKeys_PublicEntriesAndCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		value      interface{}
		valueShape string
		want       []string
		wantError  string
	}{
		{name: "key compatibility", value: []interface{}{"local"}, valueShape: dependencyValueShapePublicEntry, want: []string{"local"}},
		{name: "public entry", value: []interface{}{map[string]interface{}{"key": "local", "display_name": "Local"}}, valueShape: dependencyValueShapePublicEntry, want: []string{"local"}},
		{name: "mixed", value: []interface{}{"local", map[string]interface{}{"key": "remote"}}, valueShape: dependencyValueShapePublicEntry, want: []string{"local", "remote"}},
		{name: "objects rejected by key fields", value: []interface{}{map[string]interface{}{"key": "local"}}, valueShape: dependencyValueShapeKey, wantError: "unique strings"},
		{name: "missing key", value: []interface{}{map[string]interface{}{"display_name": "Local"}}, valueShape: dependencyValueShapePublicEntry, wantError: "non-empty key"},
		{name: "duplicate key", value: []interface{}{"local", map[string]interface{}{"key": "local"}}, valueShape: dependencyValueShapePublicEntry, wantError: "unique keys"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectedProfileKeys(test.value, "multiple", test.valueShape)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("selectedProfileKeys() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selectedProfileKeys() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPublicEntryConfigurationSchemaAcceptsRichAndKeySelections(t *testing.T) {
	schema := testDependencyBlueprint().Spec["configuration_schema"].(map[string]interface{})
	configuration := map[string]interface{}{
		"model": "local",
		"dynamic_models": []interface{}{
			map[string]interface{}{"key": "local", "display_name": "Local Model"},
			"remote",
		},
	}
	if err := validateAgainstSchema(schema, configuration); err != nil {
		t.Fatalf("rich and key dependency selections should satisfy launch schema: %v", err)
	}
}

func TestValidateDependencyAnnotation_PublicEntryRestrictions(t *testing.T) {
	tests := []struct {
		name       string
		annotation blueprintDependencyAnnotation
		property   map[string]interface{}
		want       string
	}{
		{
			name: "unknown shape",
			annotation: blueprintDependencyAnnotation{
				Selection: "multiple", ValueShape: "expanded", RequiredType: testLLMType,
				Target: blueprintDependencyTarget{Kind: "runtime_catalog", CatalogKey: "models", DependencyKey: "llm"},
			},
			property: map[string]interface{}{"type": "array", "uniqueItems": true},
			want:     "unsupported value_shape",
		},
		{
			name: "single selection",
			annotation: blueprintDependencyAnnotation{
				Selection: "single", ValueShape: dependencyValueShapePublicEntry, RequiredType: testLLMType,
				Target: blueprintDependencyTarget{Kind: "runtime_catalog", CatalogKey: "models", DependencyKey: "llm"},
			},
			property: map[string]interface{}{"type": "string"},
			want:     "multiple runtime catalog",
		},
		{
			name: "agent dependency",
			annotation: blueprintDependencyAnnotation{
				Selection: "multiple", ValueShape: dependencyValueShapePublicEntry, RequiredType: testLLMType,
				Target: blueprintDependencyTarget{Kind: "agent_dependency", AgentID: "agent", DependencyKey: "llm"},
			},
			property: map[string]interface{}{"type": "array", "uniqueItems": true},
			want:     "multiple runtime catalog",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDependencyAnnotation(nil, &store.Blueprint{}, "models", test.property, test.annotation)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateDependencyAnnotation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMaterializeBlueprintDependencySelections_OptionalAnnotatedFieldsMayBeOmitted(t *testing.T) {
	db, err := store.NewGormStore("sqlite", filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registerTestLLMAgent(t, db)

	configPath := filepath.Join(t.TempDir(), "dependencies.yaml")
	if err := os.WriteFile(configPath, []byte(`
local:
  class_name: example.LLMResolver
  provided_type: `+testLLMType+`
  catalog:
    display_name: Local Model
    capabilities: [chat]
    selectable: true
  properties:
    model: local/model
`), 0o600); err != nil {
		t.Fatal(err)
	}

	bp := testDependencyBlueprint()
	schema := bp.Spec["configuration_schema"].(map[string]interface{})
	schema["required"] = []interface{}{}
	bp.Spec["configuration"] = map[string]interface{}{}

	var spec protocol.GuildSpec
	data, _ := json.Marshal(bp.Spec)
	if err := spec.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	if err := materializeBlueprintDependencySelections(db, bp, &spec, map[string]interface{}{}, nil, configPath); err != nil {
		t.Fatalf("optional annotated fields should be omittable: %v", err)
	}
	if _, exists := spec.Agents[0].DependencyMap["llm"]; exists {
		t.Fatal("omitted optional agent dependency was unexpectedly materialized")
	}
	if _, exists := spec.Properties["dependency_selections"]; exists {
		t.Fatal("omitted optional runtime catalog was unexpectedly materialized")
	}

	schema["required"] = []interface{}{"model"}
	err = materializeBlueprintDependencySelections(db, bp, &spec, map[string]interface{}{}, nil, configPath)
	if err == nil || !strings.Contains(err.Error(), `configuration field "model" is required`) {
		t.Fatalf("missing required annotated field returned %v", err)
	}
}

func registerTestLLMAgent(t *testing.T, db store.Store) {
	t.Helper()
	requiredType := testLLMType
	if err := db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "example.LLMAgent",
		AgentName:          "LLMAgent",
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
		AgentDependencies: store.JSONB{
			"llm": map[string]interface{}{
				"dependency_key": "llm", "required_type": requiredType,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func testDependencyBlueprint() *store.Blueprint {
	annotation := map[string]interface{}{
		"selection": "single", "required_type": testLLMType,
		"filters": map[string]interface{}{"capabilities": []interface{}{"chat"}},
		"target": map[string]interface{}{
			"kind": "agent_dependency", "agent_id": "model-agent", "dependency_key": "llm",
		},
	}
	dynamicAnnotation := map[string]interface{}{
		"selection": "multiple", "value_shape": "public_entry", "required_type": testLLMType,
		"filters": map[string]interface{}{"capabilities": []interface{}{"chat"}},
		"target": map[string]interface{}{
			"kind": "runtime_catalog", "catalog_key": "dynamic_models", "dependency_key": "llm",
		},
	}
	return &store.Blueprint{
		Name: "Dependency Blueprint", Description: "test", Exposure: store.ExposurePublic, AuthorID: "author",
		Spec: store.JSONB{
			"name": "Dependency Blueprint", "description": "test", "properties": map[string]interface{}{},
			"configuration_schema": map[string]interface{}{
				"type": "object", "required": []interface{}{"model", "dynamic_models"},
				"properties": map[string]interface{}{
					"model": map[string]interface{}{"type": "string", dependencyAnnotationKey: annotation},
					"dynamic_models": map[string]interface{}{
						"type": "array", "items": map[string]interface{}{"oneOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "object", "required": []interface{}{"key"}},
						}}, "uniqueItems": true,
						dependencyAnnotationKey: dynamicAnnotation,
					},
				},
			},
			"configuration":  map[string]interface{}{"model": "local", "dynamic_models": []interface{}{"local"}},
			"dependency_map": map[string]interface{}{},
			"agents": []interface{}{map[string]interface{}{
				"id": "model-agent", "name": "Model", "description": "test", "class_name": "example.LLMAgent",
			}},
		},
	}
}
