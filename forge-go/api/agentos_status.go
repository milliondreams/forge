package api

import "github.com/rustic-ai/forge/forge-go/supervisor"

const AgentOSStatusContractVersion = 1

// AgentOSPrerequisite is a stable, machine-readable compatibility check. Names
// are contract identifiers and must not be repurposed across contract versions.
type AgentOSPrerequisite struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Satisfied bool   `json:"satisfied"`
	Detail    string `json:"detail,omitempty"`
}

type AgentOSStatusResponse struct {
	ContractVersion   int                         `json:"contractVersion"`
	AgentOSMode       bool                        `json:"agentOSMode"`
	Compatible        bool                        `json:"compatible"`
	Ready             bool                        `json:"ready"`
	Phase             string                      `json:"phase"`
	ForgeVersion      string                      `json:"forgeVersion"`
	StateSchema       int                         `json:"stateSchema"`
	Supervisor        string                      `json:"supervisor"`
	Transport         string                      `json:"transport"`
	Keychain          string                      `json:"keychain"`
	LocalModelBaseURL string                      `json:"localModelBaseURL,omitempty"`
	Prerequisites     []AgentOSPrerequisite       `json:"prerequisites"`
	Error             string                      `json:"error,omitempty"`
	Dependencies      supervisor.DependencyStatus `json:"dependencies"`
}

func agentOSCompatible(enabled bool, prerequisites []AgentOSPrerequisite, startupErr error) bool {
	if !enabled || startupErr != nil {
		return false
	}
	for _, prerequisite := range prerequisites {
		if prerequisite.Required && !prerequisite.Satisfied {
			return false
		}
	}
	return true
}
