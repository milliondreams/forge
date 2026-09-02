package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

const launchPreflightTTL = 10 * time.Minute
const maxLaunchPreflightRecords = 2048

type launchPreflightRecord struct {
	UserID       string
	OrgID        string
	BlueprintID  string
	Fingerprint  string
	Ready        bool
	ExpiresAt    time.Time
	Requirements map[string]LaunchRequirement
}

type launchPreflightCache struct {
	mu      sync.Mutex
	records map[string]launchPreflightRecord
}

func newLaunchPreflightCache() *launchPreflightCache {
	return &launchPreflightCache{records: map[string]launchPreflightRecord{}}
}

func newLaunchPreflightID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "pf_" + hex.EncodeToString(data)
}

func (s *Server) preflightCache() *launchPreflightCache {
	if s.launchPreflights == nil {
		s.launchPreflights = newLaunchPreflightCache()
	}
	return s.launchPreflights
}

func (s *Server) rememberLaunchPreflight(response *LaunchPreflightResponse, userID, orgID, blueprintID string) {
	response.ID = newLaunchPreflightID()
	expiresAt := time.Now().UTC().Add(launchPreflightTTL)
	response.ExpiresAt = expiresAt
	requirements := make(map[string]LaunchRequirement, len(response.Requirements))
	for index := range response.Requirements {
		requirement := &response.Requirements[index]
		if requirement.Action != nil {
			suffix := "secret"
			if requirement.Kind == "oauth" {
				suffix = "oauth"
			}
			requirement.Action.Href = "/rustic/catalog/launch-preflights/" + response.ID + "/requirements/" + requirement.ID + "/" + suffix
		}
		requirements[requirement.ID] = *requirement
	}

	cache := s.preflightCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for id, record := range cache.records {
		if !record.ExpiresAt.After(time.Now().UTC()) {
			delete(cache.records, id)
		}
	}
	for len(cache.records) >= maxLaunchPreflightRecords {
		var oldestID string
		var oldestExpiry time.Time
		for id, record := range cache.records {
			if oldestID == "" || record.ExpiresAt.Before(oldestExpiry) {
				oldestID = id
				oldestExpiry = record.ExpiresAt
			}
		}
		delete(cache.records, oldestID)
	}
	cache.records[response.ID] = launchPreflightRecord{
		UserID: userID, OrgID: orgID, BlueprintID: blueprintID, Fingerprint: response.Fingerprint,
		Ready: response.Ready, ExpiresAt: expiresAt, Requirements: requirements,
	}
}

func (s *Server) validateLaunchPreflight(preflightID, fingerprint, userID, orgID, blueprintID, currentFingerprint string, currentReady bool) bool {
	cache := s.preflightCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	record, exists := cache.records[preflightID]
	if !exists || !record.ExpiresAt.After(time.Now().UTC()) {
		delete(cache.records, preflightID)
		return false
	}
	return record.Ready && currentReady && record.Fingerprint == fingerprint && fingerprint == currentFingerprint &&
		record.UserID == userID && record.OrgID == orgID && record.BlueprintID == blueprintID
}

func (s *Server) launchRequirement(preflightID, requirementID string) (launchPreflightRecord, LaunchRequirement, bool) {
	cache := s.preflightCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	record, exists := cache.records[preflightID]
	if !exists || !record.ExpiresAt.After(time.Now().UTC()) {
		delete(cache.records, preflightID)
		return launchPreflightRecord{}, LaunchRequirement{}, false
	}
	requirement, exists := record.Requirements[requirementID]
	return record, requirement, exists
}

type launchSecretActionRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleLaunchSecretAction() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.secretManager == nil {
			ReplyError(w, http.StatusNotFound, "secret storage is unavailable")
			return
		}
		record, requirement, exists := s.launchRequirement(r.PathValue("preflight_id"), r.PathValue("requirement_id"))
		if !exists || requirement.Kind != "secret" || requirement.secretKey == "" {
			ReplyError(w, http.StatusNotFound, "launch requirement not found or expired")
			return
		}
		if !matchesEffectiveLaunchIdentity(r, record) {
			ReplyError(w, http.StatusForbidden, "launch requirement belongs to another principal")
			return
		}
		var request launchSecretActionRequest
		if !decodeJSONBody(w, r, &request) {
			return
		}
		value, err := decodeSecretValue(request.Value)
		if err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		err = s.secretManager.Set(record.OrgID, requirement.secretKey, value)
		if errors.Is(err, secrets.ErrSecretExists) {
			err = s.secretManager.Update(record.OrgID, requirement.secretKey, value)
		}
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to store launch credential")
			return
		}
		ReplyJSON(w, http.StatusOK, map[string]bool{"configured": true})
	}
}

func matchesEffectiveLaunchIdentity(r *http.Request, record launchPreflightRecord) bool {
	if userID := strings.TrimSpace(r.Header.Get("X-Forge-Effective-User")); userID != "" && userID != record.UserID {
		return false
	}
	if orgID := strings.TrimSpace(r.Header.Get("X-Forge-Effective-Org")); orgID != "" && orgID != record.OrgID {
		return false
	}
	return true
}

func (s *Server) handleLaunchOAuthAction() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.oauthManager == nil {
			ReplyError(w, http.StatusNotFound, "OAuth is unavailable")
			return
		}
		record, requirement, exists := s.launchRequirement(r.PathValue("preflight_id"), r.PathValue("requirement_id"))
		if !exists || requirement.Kind != "oauth" || requirement.provider == "" {
			ReplyError(w, http.StatusNotFound, "launch requirement not found or expired")
			return
		}
		if !matchesEffectiveLaunchIdentity(r, record) {
			ReplyError(w, http.StatusForbidden, "launch requirement belongs to another principal")
			return
		}
		var request authorizeRequest
		if !decodeJSONBody(w, r, &request) {
			return
		}
		clientID := strings.TrimSpace(request.ClientID)
		clientSecret := strings.TrimSpace(request.ClientSecret)
		if s.oauthManager.RequiresClientCredentials(requirement.provider) {
			if clientID == "" || clientSecret == "" {
				ReplyError(w, http.StatusUnprocessableEntity, "clientId and clientSecret are required")
				return
			}
		} else if clientID != "" || clientSecret != "" {
			ReplyError(w, http.StatusUnprocessableEntity, "this provider uses dynamic client registration; do not send client credentials")
			return
		}
		redirectURL := s.publicBaseURL() + oauthRoutePrefix(r) + "/oauth/callback"
		authURL, _, err := s.oauthManager.GetAuthURLForScopes(r.Context(), record.OrgID, requirement.provider, clientID, clientSecret, redirectURL, requirement.Scopes)
		if errors.Is(err, oauth.ErrScopeReduction) {
			ReplyError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, oauth.ErrUndeclaredScope) {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to start OAuth connection")
			return
		}
		ReplyJSON(w, http.StatusOK, authorizeResponse{AuthURL: authURL})
	}
}
