package api

import "net/http"

const (
	LaunchRequirementsV2Capability = "launch_requirements_v2"
	DependencyProfilesV1Capability = "dependency_profiles_v1"
	GuildDeletionV1Capability      = "guild_deletion_v1"
)

var rusticV1Capabilities = []string{
	LaunchRequirementsV2Capability,
	DependencyProfilesV1Capability,
}

type RusticCapabilitiesResponse struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

func (s *Server) handleGetRusticCapabilities() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		capabilities := append([]string(nil), rusticV1Capabilities...)
		if envString("FORGE_IDENTITY_MODE", "local") == "local" {
			capabilities = append(capabilities, GuildDeletionV1Capability)
		}
		ReplyJSON(w, http.StatusOK, RusticCapabilitiesResponse{
			Version:      "1",
			Capabilities: capabilities,
		})
	}
}
