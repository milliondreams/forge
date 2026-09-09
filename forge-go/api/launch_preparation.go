package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/scheduler"
)

const (
	launchPreparationTTL        = 30 * time.Minute
	maxLaunchPreparationRecords = 2048
)

type LaunchPreparationRequest struct {
	LaunchGuildFromBlueprintRequest
}

type LaunchPreparationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type launchPreparationSetupError struct {
	code string
	err  error
}

func (e *launchPreparationSetupError) Error() string { return e.err.Error() }
func (e *launchPreparationSetupError) Unwrap() error { return e.err }

type LaunchPreparationResponse struct {
	ID             string                  `json:"id"`
	Fingerprint    string                  `json:"fingerprint"`
	Status         string                  `json:"status"`
	Phase          string                  `json:"phase"`
	CompletedUnits int                     `json:"completed_units"`
	TotalUnits     int                     `json:"total_units"`
	CachedUnits    int                     `json:"cached_units"`
	ExpiresAt      time.Time               `json:"expires_at"`
	Error          *LaunchPreparationError `json:"error,omitempty"`
}

type preparedAgent struct {
	spec   protocol.AgentSpec
	nodeID string
	key    string
}

type launchPreparationRecord struct {
	response    LaunchPreparationResponse
	userID      string
	orgID       string
	blueprintID string
	guildID     string
	preflightID string
	agents      []preparedAgent
	cancel      context.CancelFunc
	consumed    bool
	released    bool
}

type launchPreparationStore struct {
	mu       sync.Mutex
	createMu sync.Mutex
	records  map[string]*launchPreparationRecord
}

func newLaunchPreparationStore() *launchPreparationStore {
	return &launchPreparationStore{records: map[string]*launchPreparationRecord{}}
}

func newLaunchPreparationID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "prep_" + hex.EncodeToString(data)
}

func (s *Server) preparationStore() *launchPreparationStore {
	if s.launchPreparations == nil {
		s.launchPreparations = newLaunchPreparationStore()
	}
	return s.launchPreparations
}

func (s *Server) handleCreateLaunchPreparation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.preparationControl == nil {
			ReplyError(w, http.StatusServiceUnavailable, "runtime preparation is unavailable")
			return
		}
		blueprintID := r.PathValue("blueprint_id")
		var request LaunchPreparationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, "invalid json")
			return
		}
		req := request.LaunchGuildFromBlueprintRequest
		if req.GuildID == nil || strings.TrimSpace(*req.GuildID) == "" || req.GuildName == "" || req.UserID == "" || req.OrgID == "" || req.PreflightID == "" || req.Fingerprint == "" {
			ReplyError(w, http.StatusUnprocessableEntity, "guild_id, guild_name, user_id, org_id, preflight_id and fingerprint are required")
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
			ReplyError(w, http.StatusForbidden, "Insufficient permissions to prepare launch")
			return
		}
		spec, err := materializeBlueprintLaunch(s.store, blueprint, req)
		if err != nil {
			ReplyError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		current, err := s.evaluateRequirements(spec, req.OrgID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to check launch requirements")
			return
		}
		if !s.validateLaunchPreflight(req.PreflightID, req.Fingerprint, req.UserID, req.OrgID, blueprintID, current.Fingerprint, current.Ready) {
			s.rememberLaunchPreflight(&current, req.UserID, req.OrgID, blueprintID)
			ReplyJSON(w, http.StatusPreconditionFailed, current)
			return
		}

		record, created, err := s.createPreparationRecord(blueprintID, req, spec)
		if err != nil {
			code := preparationFailureCode(err, "dependency_prepare_failed")
			ReplyJSON(w, http.StatusServiceUnavailable, LaunchPreparationResponse{
				Fingerprint: req.Fingerprint, Status: "failed", Phase: "complete",
				Error: &LaunchPreparationError{Code: code, Message: preparationFailureMessage(code)},
			})
			return
		}
		status := http.StatusAccepted
		response := s.launchPreparationSnapshot(record)
		if !created || response.Status == "ready" {
			status = http.StatusOK
		}
		ReplyJSON(w, status, response)
	}
}

func (s *Server) createPreparationRecord(blueprintID string, req LaunchGuildFromBlueprintRequest, spec *protocol.GuildSpec) (*launchPreparationRecord, bool, error) {
	cache := s.preparationStore()
	cache.createMu.Lock()
	defer cache.createMu.Unlock()
	now := time.Now().UTC()
	s.pruneLaunchPreparations(now)
	cache.mu.Lock()
	for _, existing := range cache.records {
		if !existing.consumed && existing.userID == req.UserID && existing.orgID == req.OrgID && existing.blueprintID == blueprintID && existing.guildID == *req.GuildID && existing.response.Fingerprint == req.Fingerprint && existing.response.Status != "failed" && existing.response.Status != "canceled" {
			cache.mu.Unlock()
			return existing, false, nil
		}
	}
	cache.mu.Unlock()

	managerRequest, err := guild.BuildGuildManagerSpawnRequest(spec, req.OrgID, req.UserID)
	if err != nil {
		return nil, false, err
	}
	agentSpecs := []protocol.AgentSpec{managerRequest.AgentSpec}
	agentSpecs = append(agentSpecs, spec.Agents...)
	agents := make([]preparedAgent, 0, len(agentSpecs))
	reg, err := registry.Load("", nil)
	if err != nil {
		return nil, false, err
	}
	for _, agentSpec := range agentSpecs {
		nodeID, scheduleErr := scheduler.GlobalScheduler.ScheduleForCapability(agentSpec, protocol.RuntimePreparationV1Capability)
		if scheduleErr != nil {
			for _, value := range agents {
				scheduler.GlobalNodeRegistry.DeallocateCapacity(value.nodeID, scheduler.RequestedCapacity(value.spec))
			}
			return nil, false, &launchPreparationSetupError{code: "prepared_node_unavailable", err: scheduleErr}
		}
		entry, lookupErr := reg.Lookup(agentSpec.ClassName)
		if lookupErr != nil {
			scheduler.GlobalNodeRegistry.DeallocateCapacity(nodeID, scheduler.RequestedCapacity(agentSpec))
			for _, value := range agents {
				scheduler.GlobalNodeRegistry.DeallocateCapacity(value.nodeID, scheduler.RequestedCapacity(value.spec))
			}
			return nil, false, lookupErr
		}
		key := ""
		if entry.Runtime == registry.RuntimeUVX {
			requirements := registry.DependencyRequirements(entry, agentSpec.ForgeExtraDeps)
			sort.Strings(requirements)
			sum := sha256.Sum256([]byte(strings.Join(requirements, "\x00")))
			key = nodeID + ":" + hex.EncodeToString(sum[:])
		}
		agents = append(agents, preparedAgent{spec: agentSpec, nodeID: nodeID, key: key})
	}

	ctx, cancel := context.WithDeadline(context.Background(), now.Add(launchPreparationTTL))
	record := &launchPreparationRecord{
		response: LaunchPreparationResponse{
			ID: newLaunchPreparationID(), Fingerprint: req.Fingerprint, Status: "queued", Phase: "python_runtime",
			ExpiresAt: now.Add(launchPreparationTTL),
		},
		userID: req.UserID, orgID: req.OrgID, blueprintID: blueprintID, guildID: *req.GuildID,
		preflightID: req.PreflightID, agents: agents, cancel: cancel,
	}
	uniqueNodes := map[string]struct{}{}
	uniqueEnvironments := map[string]struct{}{}
	for _, agent := range agents {
		uniqueNodes[agent.nodeID] = struct{}{}
		if agent.key != "" {
			uniqueEnvironments[agent.key] = struct{}{}
		}
	}
	record.response.TotalUnits = len(uniqueNodes) + len(uniqueEnvironments)

	cache.mu.Lock()
	if len(cache.records) >= maxLaunchPreparationRecords {
		cache.mu.Unlock()
		cancel()
		for _, agent := range agents {
			scheduler.GlobalNodeRegistry.DeallocateCapacity(agent.nodeID, scheduler.RequestedCapacity(agent.spec))
		}
		return nil, false, errors.New("launch preparation capacity is exhausted")
	}
	cache.records[record.response.ID] = record
	cache.mu.Unlock()
	go s.runLaunchPreparation(ctx, record)
	return record, true, nil
}

func (s *Server) pruneLaunchPreparations(now time.Time) {
	cache := s.preparationStore()
	cache.mu.Lock()
	expired := make([]*launchPreparationRecord, 0)
	for id, record := range cache.records {
		if !record.response.ExpiresAt.After(now) {
			if record.cancel != nil {
				record.cancel()
			}
			delete(cache.records, id)
			expired = append(expired, record)
		}
	}
	cache.mu.Unlock()
	for _, record := range expired {
		s.releasePreparationPlacements(record)
	}
}

func (s *Server) runLaunchPreparation(ctx context.Context, record *launchPreparationRecord) {
	s.updatePreparation(record, func(response *LaunchPreparationResponse) { response.Status = "preparing" })
	seenNodes := map[string]struct{}{}
	for _, agent := range record.agents {
		if _, exists := seenNodes[agent.nodeID]; exists {
			continue
		}
		seenNodes[agent.nodeID] = struct{}{}
		cached, err := s.prepareOnNode(ctx, record, agent.nodeID, protocol.PrepareRuntimePython, protocol.AgentSpec{})
		if err != nil {
			s.failPreparation(record, preparationFailureCode(err, "python_download_failed"))
			return
		}
		s.completePreparationUnit(record, cached)
	}
	s.updatePreparation(record, func(response *LaunchPreparationResponse) { response.Phase = "agent_environments" })
	seenEnvironments := map[string]struct{}{}
	for _, agent := range record.agents {
		if agent.key == "" {
			continue
		}
		if _, exists := seenEnvironments[agent.key]; exists {
			continue
		}
		seenEnvironments[agent.key] = struct{}{}
		cached, err := s.prepareOnNode(ctx, record, agent.nodeID, protocol.PrepareRuntimeAgentEnvironment, agent.spec)
		if err != nil {
			s.failPreparation(record, preparationFailureCode(err, "dependency_prepare_failed"))
			return
		}
		s.completePreparationUnit(record, cached)
	}
	s.finalizeLaunchPreparation(record)
}

func (s *Server) finalizeLaunchPreparation(record *launchPreparationRecord) {
	cache := s.preparationStore()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if record.response.Status != "preparing" {
		return
	}
	expiresAt := time.Now().UTC().Add(launchPreparationTTL)
	for _, agent := range record.agents {
		scheduler.GlobalPreparedPlacementMap.Reserve(scheduler.PreparedPlacement{
			PreparationID: record.response.ID, GuildID: record.guildID, AgentID: agent.spec.ID,
			NodeID: agent.nodeID, Capacity: scheduler.RequestedCapacity(agent.spec), ExpiresAt: expiresAt,
		})
	}
	record.response.Status = "ready"
	record.response.Phase = "complete"
	record.response.ExpiresAt = expiresAt
}

func preparationFailureCode(err error, fallback string) string {
	var setupError *launchPreparationSetupError
	if errors.As(err, &setupError) {
		return setupError.code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "preparation_expired"
	}
	if errors.Is(err, context.Canceled) {
		return "preparation_canceled"
	}
	return fallback
}

func (s *Server) prepareOnNode(ctx context.Context, record *launchPreparationRecord, nodeID, kind string, agentSpec protocol.AgentSpec) (bool, error) {
	requestID := "prepare-" + newLaunchPreparationID()
	payload, _ := json.Marshal(protocol.PrepareRuntimeRequest{
		RequestID: requestID, OrganizationID: record.orgID, GuildID: record.guildID, Kind: kind, AgentSpec: agentSpec,
	})
	wrapper, _ := json.Marshal(control.ControlMessageWrapper{Command: "prepare_runtime", Payload: payload})
	if err := s.preparationControl.Push(ctx, "forge:control:node:"+nodeID, wrapper); err != nil {
		return false, err
	}
	response, err := s.preparationControl.WaitResponse(ctx, requestID, launchPreparationTTL)
	if err != nil {
		return false, err
	}
	if len(response) == 0 {
		return false, errors.New("runtime preparation timed out")
	}
	var result struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Cached  bool   `json:"cached"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return false, err
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "worker rejected runtime preparation"
		}
		return false, errors.New(result.Error)
	}
	return result.Cached, nil
}

func (s *Server) updatePreparation(record *launchPreparationRecord, update func(*LaunchPreparationResponse)) {
	cache := s.preparationStore()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if current := cache.records[record.response.ID]; current != nil &&
		(current.response.Status == "queued" || current.response.Status == "preparing") {
		update(&current.response)
	}
}

func (s *Server) completePreparationUnit(record *launchPreparationRecord, cached bool) {
	s.updatePreparation(record, func(response *LaunchPreparationResponse) {
		response.CompletedUnits++
		if cached {
			response.CachedUnits++
		}
	})
}

func (s *Server) launchPreparationSnapshot(record *launchPreparationRecord) LaunchPreparationResponse {
	cache := s.preparationStore()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return record.response
}

func preparationFailureMessage(code string) string {
	switch code {
	case "python_download_failed":
		return "Python runtime preparation failed."
	case "prepared_node_unavailable":
		return "No prepared worker is available."
	case "preparation_canceled":
		return "Launch preparation was canceled."
	case "preparation_expired":
		return "Launch preparation expired."
	default:
		return "Agent runtime preparation failed."
	}
}

func (s *Server) failPreparation(record *launchPreparationRecord, code string) {
	s.releasePreparationPlacements(record)
	s.updatePreparation(record, func(response *LaunchPreparationResponse) {
		response.Status = "failed"
		response.Phase = "complete"
		response.Error = &LaunchPreparationError{Code: code, Message: preparationFailureMessage(code)}
	})
}

func (s *Server) releasePreparationPlacements(record *launchPreparationRecord) {
	cache := s.preparationStore()
	cache.mu.Lock()
	if record.released {
		cache.mu.Unlock()
		return
	}
	record.released = true
	agents := append([]preparedAgent(nil), record.agents...)
	preparationID := record.response.ID
	cache.mu.Unlock()

	placements := scheduler.GlobalPreparedPlacementMap.RemovePreparation(preparationID)
	for _, value := range placements {
		scheduler.GlobalNodeRegistry.DeallocateCapacity(value.NodeID, value.Capacity)
	}
	if len(placements) == 0 {
		for _, agent := range agents {
			scheduler.GlobalNodeRegistry.DeallocateCapacity(agent.nodeID, scheduler.RequestedCapacity(agent.spec))
		}
	}
}

func (s *Server) handleGetLaunchPreparation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cache := s.preparationStore()
		cache.mu.Lock()
		record := cache.records[r.PathValue("preparation_id")]
		if record == nil {
			cache.mu.Unlock()
			ReplyError(w, http.StatusNotFound, "launch preparation not found")
			return
		}
		if !launchPreparationIdentityMatches(r, record) {
			cache.mu.Unlock()
			ReplyError(w, http.StatusForbidden, "launch preparation belongs to another identity")
			return
		}
		expired := !record.response.ExpiresAt.After(time.Now().UTC())
		if expired {
			record.response.Status = "failed"
			record.response.Phase = "complete"
			record.response.Error = &LaunchPreparationError{Code: "preparation_expired", Message: "launch preparation expired"}
			if record.cancel != nil {
				record.cancel()
			}
		}
		response := record.response
		cache.mu.Unlock()
		if expired {
			s.releasePreparationPlacements(record)
		}
		ReplyJSON(w, http.StatusOK, response)
	}
}

func (s *Server) handleCancelLaunchPreparation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cache := s.preparationStore()
		cache.mu.Lock()
		record := cache.records[r.PathValue("preparation_id")]
		if record == nil {
			cache.mu.Unlock()
			ReplyError(w, http.StatusNotFound, "launch preparation not found")
			return
		}
		if !launchPreparationIdentityMatches(r, record) {
			cache.mu.Unlock()
			ReplyError(w, http.StatusForbidden, "launch preparation belongs to another identity")
			return
		}
		if !record.consumed && record.response.Status != "failed" && record.response.Status != "canceled" {
			record.response.Status = "canceled"
			record.response.Phase = "complete"
			record.response.Error = &LaunchPreparationError{Code: "preparation_canceled", Message: "launch preparation canceled"}
			if record.cancel != nil {
				record.cancel()
			}
		}
		cache.mu.Unlock()
		s.releasePreparationPlacements(record)
		w.WriteHeader(http.StatusNoContent)
	}
}

func launchPreparationIdentityMatches(r *http.Request, record *launchPreparationRecord) bool {
	userID := strings.TrimSpace(r.Header.Get("X-Forge-Effective-User"))
	orgID := strings.TrimSpace(r.Header.Get("X-Forge-Effective-Org"))
	return (userID == "" || userID == record.userID) && (orgID == "" || orgID == record.orgID)
}

func (s *Server) validateLaunchPreparation(id string, req LaunchGuildFromBlueprintRequest, blueprintID, fingerprint string) (*launchPreparationRecord, string) {
	cache := s.preparationStore()
	cache.mu.Lock()
	record := cache.records[id]
	if record == nil || record.consumed {
		cache.mu.Unlock()
		return nil, "preparation_expired"
	}
	if !record.response.ExpiresAt.After(time.Now().UTC()) {
		if record.response.Status != "canceled" {
			record.response.Status = "failed"
			record.response.Phase = "complete"
			record.response.Error = &LaunchPreparationError{Code: "preparation_expired", Message: "launch preparation expired"}
		}
		code := "preparation_expired"
		if record.response.Status == "canceled" {
			code = "preparation_canceled"
		}
		cache.mu.Unlock()
		s.releasePreparationPlacements(record)
		return nil, code
	}
	if record.response.Status != "ready" {
		code := "dependency_prepare_failed"
		if record.response.Error != nil && record.response.Error.Code != "" {
			code = record.response.Error.Code
		} else if record.response.Status == "canceled" {
			code = "preparation_canceled"
		} else if record.response.Status == "queued" || record.response.Status == "preparing" {
			record.response.Status = "failed"
			record.response.Phase = "complete"
			record.response.Error = &LaunchPreparationError{
				Code: code, Message: preparationFailureMessage(code),
			}
			if record.cancel != nil {
				record.cancel()
			}
		}
		cache.mu.Unlock()
		s.releasePreparationPlacements(record)
		return nil, code
	}
	if record.userID != req.UserID || record.orgID != req.OrgID || record.blueprintID != blueprintID || req.GuildID == nil || record.guildID != *req.GuildID || record.response.Fingerprint != fingerprint {
		cache.mu.Unlock()
		return nil, "dependency_prepare_failed"
	}
	agents := append([]preparedAgent(nil), record.agents...)
	cache.mu.Unlock()
	for _, agent := range agents {
		placement, found := scheduler.GlobalPreparedPlacementMap.Find(record.guildID, agent.spec.ID)
		if !found || placement.PreparationID != record.response.ID || placement.NodeID != agent.nodeID ||
			!scheduler.GlobalNodeRegistry.IsHealthy(agent.nodeID) {
			cache.mu.Lock()
			record.response.Status = "failed"
			record.response.Phase = "complete"
			record.response.Error = &LaunchPreparationError{Code: "prepared_node_unavailable", Message: "a prepared worker is unavailable"}
			cache.mu.Unlock()
			s.releasePreparationPlacements(record)
			return nil, "prepared_node_unavailable"
		}
	}
	return record, ""
}

func (s *Server) consumeLaunchPreparation(record *launchPreparationRecord) {
	cache := s.preparationStore()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if current := cache.records[record.response.ID]; current != nil {
		current.consumed = true
	}
}

func replyPreparationPrecondition(w http.ResponseWriter, code, message string) {
	ReplyJSON(w, http.StatusPreconditionFailed, map[string]interface{}{
		"detail":            message,
		"preparation_error": LaunchPreparationError{Code: code, Message: message},
	})
}

func preparationID(req LaunchGuildFromBlueprintRequest) string { return req.PreparationID }
