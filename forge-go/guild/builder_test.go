package guild_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// --- GuildBuilder fluent API tests ---

func TestGuildBuilder_Fluent_SetNameAndDescription(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("Test Guild").
		SetDescription("A test guild").
		AddAgentSpec(protocol.AgentSpec{
			Name: "Agent A", Description: "Desc A", ClassName: "ClassA",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "Test Guild" {
		t.Errorf("expected name 'Test Guild', got %s", spec.Name)
	}
	if spec.Description != "A test guild" {
		t.Errorf("expected description 'A test guild', got %s", spec.Description)
	}
}

func TestGuildBuilder_Fluent_MissingName(t *testing.T) {
	err := guild.NewGuildBuilder().
		SetDescription("desc").
		Validate()
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestGuildBuilder_Fluent_MissingDescription(t *testing.T) {
	err := guild.NewGuildBuilder().
		SetName("Test").
		Validate()
	if err == nil {
		t.Error("expected error for missing description")
	}
}

func TestGuildBuilder_Fluent_InvalidName(t *testing.T) {
	b := guild.NewGuildBuilder().SetName("")
	if err := b.Validate(); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestGuildBuilder_Fluent_SetProperty(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		SetProperty("custom_key", "custom_value").
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Properties["custom_key"] != "custom_value" {
		t.Errorf("expected custom_key=custom_value, got %v", spec.Properties["custom_key"])
	}
}

func TestGuildBuilder_Fluent_SetExecutionEngine(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		SetExecutionEngine("custom.Engine").
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Properties["execution_engine"] != "custom.Engine" {
		t.Errorf("expected custom.Engine, got %v", spec.Properties["execution_engine"])
	}
}

func TestGuildBuilder_Fluent_SetMessaging(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		SetMessaging("mod", "cls", map[string]interface{}{"url": "redis://x"}).
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msg, ok := spec.Properties["messaging"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected messaging map")
	}
	if msg["backend_module"] != "mod" {
		t.Errorf("expected backend_module=mod, got %v", msg["backend_module"])
	}
}

func TestGuildBuilder_Fluent_AddDependencyResolver(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		AddDependencyResolver("my_dep", protocol.DependencySpec{ClassName: "MyClass"}).
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep, ok := spec.DependencyMap["my_dep"]
	if !ok {
		t.Fatal("expected my_dep in dependency map")
	}
	if dep.ClassName != "MyClass" {
		t.Errorf("expected MyClass, got %s", dep.ClassName)
	}
}

func TestGuildBuilder_Fluent_LoadDependencyMapFromYAML(t *testing.T) {
	dir := t.TempDir()
	depFile := filepath.Join(dir, "deps.yaml")
	content := []byte(`
global_dep:
  class_name: "GlobalClass"
  properties:
    key: "value"
`)
	if err := os.WriteFile(depFile, content, 0644); err != nil {
		t.Fatalf("failed to write temp dep file: %v", err)
	}

	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		AddDependencyResolver("local_dep", protocol.DependencySpec{ClassName: "LocalClass"}).
		AddDependencyResolver("global_dep", protocol.DependencySpec{ClassName: "OverrideClass"}).
		LoadDependencyMapFromYAML(depFile).
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if spec.DependencyMap["local_dep"].ClassName != "LocalClass" {
		t.Errorf("expected local_dep to remain LocalClass")
	}
	if spec.DependencyMap["global_dep"].ClassName != "OverrideClass" {
		t.Errorf("expected global_dep to not be overwritten by global file")
	}
}

func TestGuildBuilder_Fluent_GatewayInjection(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		SetGateway(&protocol.GatewayConfig{
			Enabled:       true,
			InputFormats:  []string{"json"},
			OutputFormats: []string{"json"},
		}).
		AddAgentSpec(protocol.AgentSpec{
			Name: "Agent A", Description: "Desc", ClassName: "ClassA",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have original agent + injected gateway agent
	if len(spec.Agents) != 2 {
		t.Fatalf("expected 2 agents (1 user + 1 gateway), got %d", len(spec.Agents))
	}

	found := false
	for _, a := range spec.Agents {
		if a.ClassName == "rustic_ai.core.guild.g2g.gateway_agent.GatewayAgent" {
			found = true
			if a.ID != "gateway" {
				t.Errorf("expected gateway ID, got %s", a.ID)
			}
		}
	}
	if !found {
		t.Error("expected gateway agent to be injected")
	}
}

func TestGuildBuilder_Fluent_GatewayNoDuplicate(t *testing.T) {
	existingGateway := protocol.AgentSpec{
		ID:          "existing-gw",
		Name:        "Existing",
		Description: "Existing gateway",
		ClassName:   "rustic_ai.core.guild.g2g.gateway_agent.GatewayAgent",
	}

	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		SetGateway(&protocol.GatewayConfig{Enabled: true}).
		AddAgentSpec(existingGateway)

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(spec.Agents) != 1 {
		t.Fatalf("expected exactly 1 gateway agent, got %d", len(spec.Agents))
	}
	if spec.Agents[0].ID != "existing-gw" {
		t.Errorf("expected existing gateway to remain, got %s", spec.Agents[0].ID)
	}
}

func TestGuildBuilder_Fluent_AddRoute(t *testing.T) {
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		}).
		AddRoute(protocol.RoutingRule{
			AgentType: strPtr("ClassA"),
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Routes == nil || len(spec.Routes.Steps) != 1 {
		t.Fatal("expected 1 routing rule")
	}
}

func TestGuildBuilder_Fluent_DefaultsApplied(t *testing.T) {
	// BuildSpec should apply default execution_engine and messaging
	b := guild.NewGuildBuilder().
		SetName("G").
		SetDescription("D").
		AddAgentSpec(protocol.AgentSpec{
			Name: "A", Description: "D", ClassName: "C",
		})

	spec, err := b.BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.Properties["execution_engine"] != "rustic_ai.forge.ForgeExecutionEngine" {
		t.Errorf("expected default execution_engine")
	}

	msg, ok := spec.Properties["messaging"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected messaging config map")
	}
	if msg["backend_module"] != "rustic_ai.forge.messaging.redis_backend" {
		t.Errorf("expected default messaging backend")
	}
}

// --- GuildBuilder factory tests ---

func TestGuildBuilderFromSpec(t *testing.T) {
	original := &protocol.GuildSpec{
		Name:        "Original",
		Description: "Original description",
		Agents: []protocol.AgentSpec{
			{Name: "A", Description: "D", ClassName: "C"},
		},
	}

	spec, err := guild.GuildBuilderFromSpec(original).BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "Original" {
		t.Errorf("expected 'Original', got %s", spec.Name)
	}
}

func TestGuildBuilderFromSpec_Nil(t *testing.T) {
	_, err := guild.GuildBuilderFromSpec(nil).BuildSpec()
	if err == nil {
		t.Error("expected error for nil spec")
	}
}

func TestGuildBuilderFromYAMLFile(t *testing.T) {
	spec, err := guild.GuildBuilderFromYAMLFile("testdata/minimal.yaml").BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "Minimal Guild" {
		t.Errorf("expected 'Minimal Guild', got %s", spec.Name)
	}
}

func TestGuildBuilderFromYAMLFile_NotFound(t *testing.T) {
	_, err := guild.GuildBuilderFromYAMLFile("testdata/nonexistent.yaml").BuildSpec()
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestGuildBuilderFromYAML(t *testing.T) {
	yamlStr := `
id: "yaml-01"
name: "YAML Guild"
description: "From YAML string"
agents:
  - name: "A"
    description: "D"
    class_name: "C"
`
	spec, err := guild.GuildBuilderFromYAML(yamlStr).BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "YAML Guild" {
		t.Errorf("expected 'YAML Guild', got %s", spec.Name)
	}
}

func TestGuildBuilderFromJSON(t *testing.T) {
	jsonStr := `{
		"id": "json-01",
		"name": "JSON Guild",
		"description": "From JSON string",
		"agents": [{"name": "A", "description": "D", "class_name": "C"}]
	}`
	spec, err := guild.GuildBuilderFromJSON(jsonStr).BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "JSON Guild" {
		t.Errorf("expected 'JSON Guild', got %s", spec.Name)
	}
}

func TestGuildBuilderFromJSONFile(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "test.json")
	jsonContent := []byte(`{
		"id": "jf-01",
		"name": "JF Guild",
		"description": "From JSON file",
		"agents": [{"name": "A", "description": "D", "class_name": "C"}]
	}`)
	if err := os.WriteFile(jsonFile, jsonContent, 0644); err != nil {
		t.Fatalf("write json file: %v", err)
	}

	spec, err := guild.GuildBuilderFromJSONFile(jsonFile).BuildSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "JF Guild" {
		t.Errorf("expected 'JF Guild', got %s", spec.Name)
	}
}

// --- Validate tests ---

func TestValidate_Valid(t *testing.T) {
	spec := &protocol.GuildSpec{
		Name:        "Test Guild",
		Description: "Test description",
		Agents: []protocol.AgentSpec{
			{Name: "Agent A", Description: "Desc A", ClassName: "ClassA"},
			{Name: "Agent B", Description: "Desc B", ClassName: "ClassB"},
		},
	}

	if err := guild.Validate(spec); err != nil {
		t.Errorf("expected valid spec, got error: %v", err)
	}
}

func TestValidate_Invalid_NoName(t *testing.T) {
	spec := &protocol.GuildSpec{
		Description: "Test description",
		Agents:      []protocol.AgentSpec{{Name: "Agent A", Description: "Desc", ClassName: "ClassA"}},
	}
	if err := guild.Validate(spec); err == nil {
		t.Errorf("expected error for missing guild name")
	}
}

func TestValidate_Invalid_NoDescription(t *testing.T) {
	spec := &protocol.GuildSpec{
		Name:   "Test",
		Agents: []protocol.AgentSpec{{Name: "Agent A", Description: "Desc", ClassName: "ClassA"}},
	}
	if err := guild.Validate(spec); err == nil {
		t.Errorf("expected error for missing guild description")
	}
}

func TestValidate_Invalid_DuplicateAgent(t *testing.T) {
	spec := &protocol.GuildSpec{
		Name:        "Test",
		Description: "Desc",
		Agents: []protocol.AgentSpec{
			{Name: "Agent A", Description: "Desc", ClassName: "ClassA"},
			{Name: "Agent A", Description: "Desc", ClassName: "ClassB"},
		},
	}
	if err := guild.Validate(spec); err == nil {
		t.Errorf("expected error for duplicate agent name")
	}
}

func TestValidate_Invalid_MissingAgentClass(t *testing.T) {
	spec := &protocol.GuildSpec{
		Name:        "Test",
		Description: "Desc",
		Agents: []protocol.AgentSpec{
			{Name: "Agent A", Description: "Desc"}, // Missing ClassName
		},
	}
	if err := guild.Validate(spec); err == nil {
		t.Errorf("expected error for missing Agent class_name")
	}
}

func TestValidate_Invalid_NegativeResources(t *testing.T) {
	numCPUs := -1.0
	spec := &protocol.GuildSpec{
		Name:        "Test",
		Description: "Desc",
		Agents: []protocol.AgentSpec{
			{
				Name:        "Agent A",
				Description: "Desc",
				ClassName:   "ClassA",
				Resources: protocol.ResourceSpec{
					NumCPUs: &numCPUs,
				},
			},
		},
	}
	if err := guild.Validate(spec); err == nil {
		t.Errorf("expected error for negative resource values")
	}
}

// --- Template resolution tests (via BuildSpec) ---

func TestGuildBuilder_FromYAMLWithVar(t *testing.T) {
	builtSpec, err := guild.GuildBuilderFromYAMLFile("testdata/test_guild_with_variables.yaml").BuildSpec()
	if err != nil {
		t.Fatalf("failed to build templated spec: %v", err)
	}

	if builtSpec.Name != "test_guild_name" {
		t.Errorf("expected test_guild_name, got %s", builtSpec.Name)
	}

	if builtSpec.Description != "description for test_guild_name" {
		t.Errorf("expected description for test_guild_name, got %s", builtSpec.Description)
	}

	agent := builtSpec.Agents[0]
	if agent.Name != "asdf" {
		t.Errorf("expected agent name 'asdf', got '%s'", agent.Name)
	}

	if agent.Description != "asdf - A simple agent with variable properties" {
		t.Errorf("expected templated description, got '%s'", agent.Description)
	}

	if agent.ClassName != "guild.simple_agent.SimpleAgentWithProps" {
		t.Errorf("expected class_name 'guild.simple_agent.SimpleAgentWithProps', got '%s'", agent.ClassName)
	}

	if agent.Properties["prop1"] != "custom/model_1" {
		t.Errorf("expected prop1 'custom/model_1', got '%v'", agent.Properties["prop1"])
	}

	prop2 := agent.Properties["prop2"]
	if strP, ok := prop2.(string); ok && strP == "2" {
		// Native mustache string replacement OK
	} else if numP, ok := prop2.(float64); ok && numP == 2 {
		// If json re-marshaling somehow figures out it's a number
	} else {
		t.Errorf("expected prop2 '2' or 2, got '%v' type %T", prop2, prop2)
	}
}

func TestGuildBuilder_FromJSONWithVar(t *testing.T) {
	builtSpec, err := guild.GuildBuilderFromYAMLFile("testdata/test_guild_with_variables.json").BuildSpec()
	if err != nil {
		t.Fatalf("failed to build templated json spec: %v", err)
	}

	if builtSpec.Name != "test_guild_name" {
		t.Errorf("expected test_guild_name, got %s", builtSpec.Name)
	}

	agent := builtSpec.Agents[0]
	if agent.Name != "asdf" {
		t.Errorf("expected agent name 'asdf', got '%s'", agent.Name)
	}

	if agent.Description != "asdf - A simple agent with variable properties" {
		t.Errorf("expected templated description, got '%s'", agent.Description)
	}

	if agent.Properties["prop1"] != "custom/model_1" {
		t.Errorf("expected prop1 'custom/model_1', got '%v'", agent.Properties["prop1"])
	}
}

func TestRenderConfiguration_UsesExistingMustacheForPublicEntries(t *testing.T) {
	spec := &protocol.GuildSpec{
		Configuration: map[string]interface{}{
			"dynamic_models": []interface{}{
				map[string]interface{}{
					"key":          "local-balanced",
					"display_name": "Balanced Local Model",
					"aliases":      []interface{}{"standard", "medium"},
				},
				"legacy-key",
			},
		},
		Agents: []protocol.AgentSpec{{
			ID: "analyzer", Name: "Analyzer", Description: "test", ClassName: "example.Agent",
			Properties: map[string]interface{}{
				"default_system_prompt": "Enabled:\n{{#dynamic_models}}{{#key}}- {{key}}: {{display_name}} [{{#aliases}}{{.}};{{/aliases}}]\n{{/key}}{{^key}}- {{.}}\n{{/key}}{{/dynamic_models}}",
			},
		}},
	}

	rendered, err := guild.RenderConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	prompt := rendered.Agents[0].Properties["default_system_prompt"].(string)
	for _, expected := range []string{"local-balanced", "Balanced Local Model", "standard;medium;", "legacy-key"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("rendered prompt %q does not contain %q", prompt, expected)
		}
	}
}

// --- Error chaining tests ---

func TestGuildBuilder_ErrorChaining(t *testing.T) {
	// Setting empty name should store error, subsequent setters should be no-ops
	b := guild.NewGuildBuilder().
		SetName("").
		SetDescription("should not matter")

	_, err := b.BuildSpec()
	if err == nil {
		t.Error("expected error from empty name in chain")
	}
}

func strPtr(s string) *string { return &s }
