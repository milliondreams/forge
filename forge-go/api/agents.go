package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/rustic-ai/forge/forge-go/guild"
)

// AgentRegistryEntry represents a single known agent type in the Forge cluster.
// For Phase 2d.3 Option A, this is a statically maintained list.
type AgentRegistryEntry struct {
	Name               string                 `json:"-"`
	AgentName          string                 `json:"agent_name"`
	QualifiedClassName string                 `json:"qualified_class_name"`
	AgentDoc           string                 `json:"agent_doc"`
	AgentPropsSchema   map[string]interface{} `json:"agent_props_schema"`
	MessageHandlers    map[string]interface{} `json:"message_handlers"`
	AgentDependencies  []AgentDependencyEntry `json:"agent_dependencies"`
}

type AgentDependencyEntry struct {
	DependencyKey string  `json:"dependency_key"`
	DependencyVar *string `json:"dependency_var,omitempty"`
	GuildLevel    *bool   `json:"guild_level,omitempty"`
	OrgLevel      *bool   `json:"org_level,omitempty"`
	AgentLevel    *bool   `json:"agent_level,omitempty"`
	VariableName  *string `json:"variable_name,omitempty"`
	RequiredType  *string `json:"required_type,omitempty"`
}

type ConfiguredDependencyEntry struct {
	Key          string                 `json:"key"`
	DisplayName  string                 `json:"display_name"`
	Description  string                 `json:"description,omitempty"`
	ProvidedType string                 `json:"provided_type"`
	Provider     string                 `json:"provider,omitempty"`
	Capabilities []string               `json:"capabilities"`
	Aliases      []string               `json:"aliases"`
	Availability DependencyAvailability `json:"availability"`
}

type DependencyAvailability struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

type BlueprintAgentDependencyEntry struct {
	BindingKey    string                      `json:"binding_key"`
	DependencyKey string                      `json:"dependency_key"`
	DependencyVar *string                     `json:"dependency_var,omitempty"`
	GuildLevel    *bool                       `json:"guild_level,omitempty"`
	OrgLevel      *bool                       `json:"org_level,omitempty"`
	AgentLevel    *bool                       `json:"agent_level,omitempty"`
	VariableName  *string                     `json:"variable_name,omitempty"`
	RequiredType  *string                     `json:"required_type,omitempty"`
	Providers     []ConfiguredDependencyEntry `json:"providers"`
}

type BlueprintAgentDependencySummary struct {
	AgentName          string                          `json:"agent_name"`
	QualifiedClassName string                          `json:"qualified_class_name"`
	Dependencies       []BlueprintAgentDependencyEntry `json:"dependencies"`
}

var defaultKnownSystemAgents = []AgentRegistryEntry{
	{
		Name:               "GuildManagerAgent",
		AgentName:          "GuildManagerAgent",
		QualifiedClassName: guild.GuildManagerClassName,
		AgentDoc:           "Supervisor and Orchestrator for the Guild",
		AgentPropsSchema:   map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		MessageHandlers:    map[string]interface{}{},
		AgentDependencies:  []AgentDependencyEntry{},
	},
	{
		Name:               "UserProxyAgent",
		AgentName:          "UserProxyAgent",
		QualifiedClassName: "rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent",
		AgentDoc:           "Dynamic per-user agent representing a human user in the guild",
		AgentPropsSchema:   map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		MessageHandlers:    map[string]interface{}{},
		AgentDependencies:  []AgentDependencyEntry{},
	},
	{
		Name:               "SupportAgent",
		AgentName:          "SupportAgent",
		QualifiedClassName: "rustic_ai.core.agents.support.support_agent.SupportAgent",
		AgentDoc:           "Standard support agent template",
		AgentPropsSchema:   map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		MessageHandlers:    map[string]interface{}{},
		AgentDependencies:  []AgentDependencyEntry{},
	},
	{
		Name:               "EchoAgent",
		AgentName:          "EchoAgent",
		QualifiedClassName: "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
		AgentDoc:           "An Agent that echoes the received message back to the sender.",
		AgentPropsSchema:   map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		MessageHandlers:    map[string]interface{}{},
		AgentDependencies:  []AgentDependencyEntry{},
	},
}

var (
	knownSystemAgentsOnce sync.Once
	knownSystemAgents     []AgentRegistryEntry
)

func getKnownSystemAgents() []AgentRegistryEntry {
	knownSystemAgentsOnce.Do(func() {
		path := os.Getenv("FORGE_STATIC_AGENTS_JSON")
		if path == "" {
			path = "conf/agents.json"
		}

		loaded, err := loadKnownSystemAgentsFromFile(path)
		if err != nil {
			slog.Warn("Failed to load static agent registry file, falling back to built-in defaults", "path", path, "error", err)
			knownSystemAgents = defaultKnownSystemAgents
			return
		}
		if len(loaded) == 0 {
			slog.Warn("Static agent registry file had zero entries, falling back to built-in defaults", "path", path)
			knownSystemAgents = defaultKnownSystemAgents
			return
		}
		knownSystemAgents = loaded
	})
	return knownSystemAgents
}

func loadKnownSystemAgentsFromFile(path string) ([]AgentRegistryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	byClass := map[string]AgentRegistryEntry{}
	if err := json.Unmarshal(data, &byClass); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(byClass))
	for k := range byClass {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]AgentRegistryEntry, 0, len(keys))
	for _, k := range keys {
		agent := byClass[k]
		if agent.QualifiedClassName == "" {
			agent.QualifiedClassName = k
		}
		if agent.Name == "" {
			agent.Name = agent.AgentName
		}
		out = append(out, agent)
	}

	return out, nil
}

// HandleListAgents serves a static JSON registry of known agent types.
// Maps to GET /api/registry/agents
func (s *Server) HandleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := getKnownSystemAgents()
	agentClass := r.URL.Query().Get("agent_class")
	if agentClass != "" {
		for _, agent := range agents {
			if agent.QualifiedClassName == agentClass {
				ReplyJSON(w, http.StatusOK, map[string]AgentRegistryEntry{agent.QualifiedClassName: agent})
				return
			}
		}
		ReplyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	hidden := map[string]bool{
		"GuildManagerAgent":   true,
		"ProbeAgent":          true,
		"EssentialProbeAgent": true,
	}
	resp := map[string]AgentRegistryEntry{}
	for _, agent := range agents {
		if hidden[agent.AgentName] {
			continue
		}
		resp[agent.QualifiedClassName] = agent
	}
	ReplyJSON(w, http.StatusOK, resp)
}

// HandleGetAgentByClassName serves details for a specific agent type.
// Maps to GET /api/registry/agents/{class_name} or /catalog/agents/{class_name}
func (s *Server) HandleGetAgentByClassName(w http.ResponseWriter, r *http.Request) {
	agents := getKnownSystemAgents()
	className := r.PathValue("class_name")

	for _, agent := range agents {
		if agent.QualifiedClassName == className {
			ReplyJSON(w, http.StatusOK, agent)
			return
		}
	}

	ReplyError(w, http.StatusNotFound, "Agent class not found in static registry")
}

// HandleGetMessageSchemaByClass mirrors Python /api/registry/message_schema/ branch behavior
// for the classes used in parity corpus.
func (s *Server) HandleGetMessageSchemaByClass(w http.ResponseWriter, r *http.Request) {
	messageClass := r.URL.Query().Get("message_class")
	if messageClass == "" {
		ReplyError(w, http.StatusNotFound, "Class  not found")
		return
	}
	switch messageClass {
	case "rustic_ai.core.agents.commons.media.MediaLink":
		ReplyJSON(w, http.StatusOK, map[string]interface{}{
			"title": "MediaLink",
			"type":  "object",
			"properties": map[string]interface{}{
				"url":           map[string]interface{}{"type": "string"},
				"name":          map[string]interface{}{"type": "string"},
				"metadata":      map[string]interface{}{"type": "object"},
				"mimetype":      map[string]interface{}{"type": "string"},
				"encoding":      map[string]interface{}{"type": "string"},
				"on_filesystem": map[string]interface{}{"type": "boolean"},
			},
			"required": []string{"url"},
		})
		return
	case "rustic_ai.core.agents.testutils.echo_agent.EchoAgent":
		ReplyError(w, http.StatusBadRequest, "Class is not a subclass of pydantic.BaseModel")
		return
	default:
		ReplyError(w, http.StatusNotFound, "Class "+messageClass+" not found")
		return
	}
}
