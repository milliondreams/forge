package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

type LaunchRequirementAction struct {
	Kind                      string `json:"kind"`
	Method                    string `json:"method"`
	Href                      string `json:"href"`
	RequiresClientCredentials bool   `json:"requires_client_credentials,omitempty"`
}

type LaunchRequirement struct {
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	Label         string                        `json:"label"`
	Optional      bool                          `json:"optional"`
	Status        string                        `json:"status"`
	Scopes        []string                      `json:"scopes,omitempty"`
	MissingScopes []string                      `json:"missing_scopes,omitempty"`
	Sources       []CredentialRequirementSource `json:"sources"`
	Action        *LaunchRequirementAction      `json:"action,omitempty"`
	secretKey     string
	provider      string
}

type LaunchPreflightResponse struct {
	ID           string              `json:"id"`
	Fingerprint  string              `json:"fingerprint"`
	Status       string              `json:"status"`
	Ready        bool                `json:"ready"`
	ExpiresAt    time.Time           `json:"expires_at"`
	Requirements []LaunchRequirement `json:"requirements"`
}

func opaqueRequirementID(kind, identity string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + identity))
	return "req_" + hex.EncodeToString(sum[:12])
}

func (s *Server) evaluateRequirements(spec *protocol.GuildSpec, orgID string) (LaunchPreflightResponse, error) {
	if s.credentialRegistry == nil {
		reg, err := registry.Load("", s.oauthManager)
		if err != nil {
			return LaunchPreflightResponse{}, err
		}
		s.credentialRegistry = reg
	}
	resolved, err := resolveGuildCredentialRequirements(spec, s.credentialRegistry, dependencyConfigPath())
	if err != nil {
		return LaunchPreflightResponse{}, err
	}

	secretRequirements := map[string]*LaunchRequirement{}
	oauthRequirements := map[string]*LaunchRequirement{}
	secretMandatory := map[string]bool{}
	oauthMandatory := map[string]bool{}
	for _, agent := range resolved {
		for _, secret := range agent.Secrets {
			requirement := secretRequirements[secret.Need.Key]
			if requirement == nil {
				requirement = &LaunchRequirement{
					ID: opaqueRequirementID("secret", secret.Need.Key), Kind: "secret", Label: secret.Need.Label,
					Scopes: []string{}, Sources: []CredentialRequirementSource{}, secretKey: secret.Need.Key,
				}
				secretRequirements[secret.Need.Key] = requirement
			} else if requirement.Label != secret.Need.Label {
				return LaunchPreflightResponse{}, fmt.Errorf("conflicting labels for secret %q", secret.Need.Key)
			}
			secretMandatory[secret.Need.Key] = secretMandatory[secret.Need.Key] || !isOptional(secret.Need.Optional)
			for _, source := range secret.Sources {
				requirement.Sources = appendSource(requirement.Sources, source)
			}
		}
		for _, oauthNeed := range agent.OAuth {
			requirement := oauthRequirements[oauthNeed.Need.Provider]
			if requirement == nil {
				requirement = &LaunchRequirement{
					ID: opaqueRequirementID("oauth", oauthNeed.Need.Provider), Kind: "oauth", Label: oauthNeed.Need.Label,
					Scopes: []string{}, Sources: []CredentialRequirementSource{}, provider: oauthNeed.Need.Provider,
				}
				oauthRequirements[oauthNeed.Need.Provider] = requirement
			} else if requirement.Label != oauthNeed.Need.Label {
				return LaunchPreflightResponse{}, fmt.Errorf("conflicting labels for OAuth provider %q", oauthNeed.Need.Provider)
			}
			oauthMandatory[oauthNeed.Need.Provider] = oauthMandatory[oauthNeed.Need.Provider] || !isOptional(oauthNeed.Need.Optional)
			for _, scope := range oauthNeed.Need.Scopes {
				if !containsFold(requirement.Scopes, scope) {
					requirement.Scopes = append(requirement.Scopes, scope)
				}
			}
			for _, source := range oauthNeed.Sources {
				requirement.Sources = appendSource(requirement.Sources, source)
			}
		}
	}

	identities := make([]string, 0, len(secretRequirements)+len(oauthRequirements))
	for key := range secretRequirements {
		identities = append(identities, "secret\x00"+key)
	}
	for provider := range oauthRequirements {
		identities = append(identities, "oauth\x00"+provider)
	}
	sort.Strings(identities)
	response := LaunchPreflightResponse{Ready: true, Status: "ready", Requirements: []LaunchRequirement{}}
	for _, identity := range identities {
		parts := strings.SplitN(identity, "\x00", 2)
		var requirement *LaunchRequirement
		if parts[0] == "secret" {
			requirement = secretRequirements[parts[1]]
			requirement.Optional = !secretMandatory[parts[1]]
			if s.secretManager != nil && s.secretManager.Exists(orgID, requirement.secretKey) {
				requirement.Status = "configured"
				requirement.Action = &LaunchRequirementAction{Kind: "replace_secret", Method: http.MethodPost}
			} else {
				requirement.Status = "missing"
				requirement.Action = &LaunchRequirementAction{Kind: "set_secret", Method: http.MethodPost}
			}
		} else {
			requirement = oauthRequirements[parts[1]]
			requirement.Optional = !oauthMandatory[parts[1]]
			sort.Strings(requirement.Scopes)
			if s.oauthManager == nil || !s.oauthManager.ProviderExists(requirement.provider) {
				requirement.Status = "provider_unavailable"
			} else {
				ready, missing, err := s.oauthManager.ConnectionSatisfiesScopes(orgID, requirement.provider, requirement.Scopes)
				if err != nil {
					return LaunchPreflightResponse{}, err
				}
				if ready {
					requirement.Status = "configured"
					requirement.Action = &LaunchRequirementAction{Kind: "reconnect_oauth", Method: http.MethodPost, RequiresClientCredentials: s.oauthManager.RequiresClientCredentials(requirement.provider)}
				} else {
					requirement.MissingScopes = missing
					connected, err := s.oauthManager.IsConnected(orgID, requirement.provider)
					if err != nil {
						return LaunchPreflightResponse{}, err
					}
					requirement.Status = "missing"
					if connected {
						requirement.Status = "insufficient_scope"
					}
					requirement.Action = &LaunchRequirementAction{Kind: "connect_oauth", Method: http.MethodPost, RequiresClientCredentials: s.oauthManager.RequiresClientCredentials(requirement.provider)}
				}
			}
		}
		if !requirement.Optional && requirement.Status != "configured" {
			response.Ready = false
			response.Status = "blocked"
		}
		response.Requirements = append(response.Requirements, *requirement)
	}
	response.Fingerprint = materializationFingerprint(spec, orgID)
	return response, nil
}

func materializationFingerprint(spec *protocol.GuildSpec, orgID string) string {
	var normalized map[string]interface{}
	raw, _ := json.Marshal(spec)
	_ = json.Unmarshal(raw, &normalized)
	delete(normalized, "name")
	snapshot, _ := json.Marshal(struct {
		OrganizationID string                 `json:"organization_id"`
		Spec           map[string]interface{} `json:"spec"`
	}{OrganizationID: orgID, Spec: normalized})
	sum := sha256.Sum256(snapshot)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Server) handlePreflightGuildFromBlueprint() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blueprintID := r.PathValue("blueprint_id")
		var req LaunchGuildFromBlueprintRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		if req.GuildID == nil || strings.TrimSpace(*req.GuildID) == "" || req.GuildName == "" || req.UserID == "" || req.OrgID == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "guild_id, guild_name, user_id and org_id are required")
			return
		}
		blueprint, err := s.store.GetBlueprint(blueprintID)
		if err != nil {
			if err == store.ErrNotFound {
				ReplyError(w, http.StatusNotFound, "Blueprint not found")
			} else {
				ReplyError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		allowed, err := canLaunchBlueprint(s.store, blueprint, req.UserID, req.OrgID)
		if err != nil || !allowed {
			if err != nil {
				ReplyError(w, http.StatusInternalServerError, err.Error())
			} else {
				ReplyError(w, http.StatusForbidden, "Insufficient permissions to launch")
			}
			return
		}
		spec, err := materializeBlueprintLaunch(s.store, blueprint, req)
		if err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		response, err := s.evaluateRequirements(spec, req.OrgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to check launch requirements: "+err.Error())
			return
		}
		s.rememberLaunchPreflight(&response, req.UserID, req.OrgID, blueprintID)
		ReplyJSON(w, http.StatusOK, response)
	}
}
