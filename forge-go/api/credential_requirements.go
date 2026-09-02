package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

// CredentialRequirementSource identifies the Forge-owned declaration that
// contributed a credential requirement. Storage keys and environment labels
// deliberately never appear in the public preflight representation.
type CredentialRequirementSource struct {
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	Origin      string `json:"origin"`
	ProfileKey  string `json:"profile_key,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

type resolvedSecretRequirement struct {
	Need    protocol.SecretNeed
	Sources []CredentialRequirementSource
}

type resolvedOAuthRequirement struct {
	Need    protocol.OAuthNeed
	Sources []CredentialRequirementSource
}

type resolvedAgentCredentialRequirements struct {
	AgentID   string
	AgentName string
	Secrets   []resolvedSecretRequirement
	OAuth     []resolvedOAuthRequirement
}

func (r resolvedAgentCredentialRequirements) requirements() protocol.CredentialRequirements {
	out := protocol.NewCredentialRequirements()
	for _, secret := range r.Secrets {
		out.Secrets = append(out.Secrets, secret.Need)
	}
	for _, need := range r.OAuth {
		out.OAuth = append(out.OAuth, need.Need)
	}
	return out
}

func resolveGuildCredentialRequirements(
	spec *protocol.GuildSpec,
	reg *registry.Registry,
	configPath string,
) ([]resolvedAgentCredentialRequirements, error) {
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return nil, err
	}
	resolved := make([]resolvedAgentCredentialRequirements, 0, len(spec.Agents))
	for index := range spec.Agents {
		agentRequirements, err := resolveAgentCredentialRequirements(&spec.Agents[index], reg, profiles)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", spec.Agents[index].Name, err)
		}
		resolved = append(resolved, agentRequirements)
	}
	return resolved, nil
}

// ResolveAgentCredentialRequirements resolves one agent against the supplied
// Forge registry and dependency profile configuration.
func ResolveAgentCredentialRequirements(agent *protocol.AgentSpec, reg *registry.Registry, configPath string) (protocol.CredentialRequirements, error) {
	if strings.TrimSpace(configPath) == "" {
		configPath = dependencyConfigPath()
	}
	profiles, err := loadConfiguredDependencyProfiles(configPath)
	if err != nil {
		return protocol.CredentialRequirements{}, err
	}
	resolved, err := resolveAgentCredentialRequirements(agent, reg, profiles)
	if err != nil {
		return protocol.CredentialRequirements{}, err
	}
	return resolved.requirements(), nil
}

func validateCredentialCatalog(reg *registry.Registry, profiles map[string]configuredDependencyProfile) error {
	secretLabels := map[string]string{}
	oauthLabels := map[string]string{}
	validate := func(owner string, requirements protocol.CredentialRequirements) error {
		for _, secret := range requirements.Secrets {
			if existing, exists := secretLabels[secret.Key]; exists && existing != secret.Label {
				return fmt.Errorf("%s uses label %q for secret %q; expected %q", owner, secret.Label, secret.Key, existing)
			}
			secretLabels[secret.Key] = secret.Label
		}
		for _, need := range requirements.OAuth {
			if existing, exists := oauthLabels[need.Provider]; exists && existing != need.Label {
				return fmt.Errorf("%s uses label %q for OAuth provider %q; expected %q", owner, need.Label, need.Provider, existing)
			}
			oauthLabels[need.Provider] = need.Label
		}
		return nil
	}
	for _, className := range reg.ClassNames() {
		entry, err := reg.Lookup(className)
		if err != nil {
			return err
		}
		if err := validate("agent "+className, entry.Requirements); err != nil {
			return err
		}
	}
	for key, profile := range profiles {
		if err := validate("dependency profile "+key, profile.Requirements); err != nil {
			return err
		}
	}
	return nil
}

func resolveAgentCredentialRequirements(
	agent *protocol.AgentSpec,
	reg *registry.Registry,
	profiles map[string]configuredDependencyProfile,
) (resolvedAgentCredentialRequirements, error) {
	if agent == nil {
		return resolvedAgentCredentialRequirements{}, fmt.Errorf("agent is required")
	}
	entry, err := reg.Lookup(agent.ClassName)
	if err != nil {
		return resolvedAgentCredentialRequirements{}, err
	}

	out := resolvedAgentCredentialRequirements{AgentID: agent.ID, AgentName: agent.Name}
	secretByKey := map[string]int{}
	oauthByProvider := map[string]int{}
	add := func(requirements protocol.CredentialRequirements, source CredentialRequirementSource) error {
		requirements.Normalize()
		if err := requirements.Validate(); err != nil {
			return err
		}
		for _, secret := range requirements.Secrets {
			if existingIndex, exists := secretByKey[secret.Key]; exists {
				existing := &out.Secrets[existingIndex]
				if existing.Need.Env != secret.Env || existing.Need.Label != secret.Label {
					return fmt.Errorf("conflicting declarations for secret %q", secret.Key)
				}
				existing.Need.Optional = combinedOptional(existing.Need.Optional, secret.Optional)
				existing.Sources = appendSource(existing.Sources, source)
				continue
			}
			secretByKey[secret.Key] = len(out.Secrets)
			out.Secrets = append(out.Secrets, resolvedSecretRequirement{Need: secret, Sources: []CredentialRequirementSource{source}})
		}
		for _, need := range requirements.OAuth {
			if existingIndex, exists := oauthByProvider[need.Provider]; exists {
				existing := &out.OAuth[existingIndex]
				if existing.Need.Env != need.Env || existing.Need.Label != need.Label {
					return fmt.Errorf("conflicting declarations for OAuth provider %q", need.Provider)
				}
				existing.Need.Optional = combinedOptional(existing.Need.Optional, need.Optional)
				for _, scope := range need.Scopes {
					if !containsFold(existing.Need.Scopes, scope) {
						existing.Need.Scopes = append(existing.Need.Scopes, scope)
					}
				}
				sort.Strings(existing.Need.Scopes)
				existing.Sources = appendSource(existing.Sources, source)
				continue
			}
			sort.Strings(need.Scopes)
			oauthByProvider[need.Provider] = len(out.OAuth)
			out.OAuth = append(out.OAuth, resolvedOAuthRequirement{Need: need, Sources: []CredentialRequirementSource{source}})
		}
		return nil
	}

	agentSource := CredentialRequirementSource{
		AgentID: agent.ID, AgentName: agent.Name, Origin: "agent",
	}
	if err := add(entry.Requirements, agentSource); err != nil {
		return resolvedAgentCredentialRequirements{}, fmt.Errorf("agent registry requirements: %w", err)
	}
	for _, key := range agentProfileKeys(agent) {
		profile, exists := profiles[key]
		if !exists {
			return resolvedAgentCredentialRequirements{}, fmt.Errorf("selected dependency profile %q does not exist", key)
		}
		profileName := strings.TrimSpace(profile.Catalog.DisplayName)
		if profileName == "" {
			profileName = key
		}
		if err := add(profile.Requirements, CredentialRequirementSource{
			AgentID: agent.ID, AgentName: agent.Name, Origin: "profile", ProfileKey: key, ProfileName: profileName,
		}); err != nil {
			return resolvedAgentCredentialRequirements{}, fmt.Errorf("profile %q requirements: %w", key, err)
		}
	}

	sort.Slice(out.Secrets, func(i, j int) bool { return out.Secrets[i].Need.Key < out.Secrets[j].Need.Key })
	sort.Slice(out.OAuth, func(i, j int) bool { return out.OAuth[i].Need.Provider < out.OAuth[j].Need.Provider })
	return out, nil
}

func combinedOptional(left, right *bool) *bool {
	optional := isOptional(left) && isOptional(right)
	return &optional
}

func isOptional(value *bool) bool {
	return value != nil && *value
}

func appendSource(sources []CredentialRequirementSource, source CredentialRequirementSource) []CredentialRequirementSource {
	for _, existing := range sources {
		if existing == source {
			return sources
		}
	}
	return append(sources, source)
}
