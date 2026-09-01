package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const forceGuildShutdownTimeout = 10 * time.Second

type guildDeletionError struct {
	Code           string   `json:"code"`
	Detail         string   `json:"detail"`
	ActiveAgentIDs []string `json:"active_agent_ids,omitempty"`
	Stage          string   `json:"stage,omitempty"`
}

func (s *Server) HandleDeleteGuild(w http.ResponseWriter, r *http.Request) {
	if envString("FORGE_IDENTITY_MODE", "local") != "local" {
		ReplyJSON(w, http.StatusNotImplemented, guildDeletionError{Code: "hosted_auth_required", Detail: "guild deletion requires hosted token validation"})
		return
	}
	guildID := r.PathValue("id")
	if err := guild.ValidateID(guildID); err != nil {
		ReplyJSON(w, http.StatusUnprocessableEntity, guildDeletionError{Code: "invalid_guild_id", Detail: err.Error()})
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	orgID := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if userID == "" || orgID == "" {
		ReplyJSON(w, http.StatusUnprocessableEntity, guildDeletionError{Code: "missing_identity", Detail: "user_id and org_id are required"})
		return
	}
	force, err := strconv.ParseBool(defaultString(r.URL.Query().Get("force"), "false"))
	if err != nil {
		ReplyJSON(w, http.StatusUnprocessableEntity, guildDeletionError{Code: "invalid_force", Detail: "force must be a boolean"})
		return
	}

	model, err := s.store.GetGuild(guildID)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		ReplyJSON(w, http.StatusInternalServerError, guildDeletionError{Code: "guild_lookup_failed", Detail: "failed to load guild"})
		return
	}
	if model.OrganizationID != orgID || model.CreatedBy != userID {
		ReplyJSON(w, http.StatusForbidden, guildDeletionError{Code: "guild_delete_forbidden", Detail: "only the guild creator in its owning organization may delete it"})
		return
	}

	active, err := s.activeGuildWorkloads(r.Context(), model)
	if err != nil {
		ReplyJSON(w, http.StatusInternalServerError, guildDeletionError{Code: "guild_status_failed", Detail: "failed to inspect guild runtime state"})
		return
	}
	if len(active) > 0 && !force {
		ReplyJSON(w, http.StatusConflict, guildDeletionError{Code: "guild_not_quiescent", Detail: "shut down the guild before deleting it", ActiveAgentIDs: active})
		return
	}
	if len(active) > 0 {
		if err := s.forceStopGuild(r.Context(), model); err != nil {
			ReplyJSON(w, http.StatusConflict, guildDeletionError{Code: "force_shutdown_incomplete", Detail: err.Error(), ActiveAgentIDs: active})
			return
		}
	}

	if err := s.purgeGuildData(r.Context(), model); err != nil {
		ReplyJSON(w, http.StatusInternalServerError, guildDeletionError{Code: "guild_delete_failed", Detail: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func guildWorkloadIDs(model *store.GuildModel) []string {
	ids := []string{model.ID + "#manager_agent"}
	for _, agent := range model.Agents {
		ids = append(ids, agent.ID)
	}
	sort.Strings(ids)
	return ids
}

func (s *Server) activeGuildWorkloads(ctx context.Context, model *store.GuildModel) ([]string, error) {
	active := make([]string, 0)
	for _, agentID := range guildWorkloadIDs(model) {
		status, err := s.statusStore.GetStatus(ctx, model.ID, agentID)
		if err != nil {
			return nil, err
		}
		if status != nil {
			active = append(active, agentID)
		}
	}
	return active, nil
}

func (s *Server) forceStopGuild(ctx context.Context, model *store.GuildModel) error {
	if s.controlPusher == nil {
		return fmt.Errorf("Forge control plane is unavailable")
	}
	managerID := model.ID + "#manager_agent"
	if err := protocol.PushStopRequest(ctx, s.controlPusher, protocol.StopRequest{RequestID: "delete-" + idgen.NewShortUUID(), OrganizationID: model.OrganizationID, GuildID: model.ID, AgentID: managerID}); err != nil {
		return fmt.Errorf("stop guild manager: %w", err)
	}
	deadline := time.Now().Add(forceGuildShutdownTimeout)
	if err := s.waitForWorkloadsGone(ctx, model.ID, []string{managerID}, deadline); err != nil {
		return err
	}

	childIDs := make([]string, 0, len(model.Agents))
	for _, agent := range model.Agents {
		childIDs = append(childIDs, agent.ID)
		if err := protocol.PushStopRequest(ctx, s.controlPusher, protocol.StopRequest{RequestID: "delete-" + idgen.NewShortUUID(), OrganizationID: model.OrganizationID, GuildID: model.ID, AgentID: agent.ID}); err != nil {
			return fmt.Errorf("stop agent %s: %w", agent.ID, err)
		}
	}
	return s.waitForWorkloadsGone(ctx, model.ID, childIDs, deadline)
}

func (s *Server) waitForWorkloadsGone(ctx context.Context, guildID string, agentIDs []string, deadline time.Time) error {
	for {
		remaining := make([]string, 0)
		for _, agentID := range agentIDs {
			status, err := s.statusStore.GetStatus(ctx, guildID, agentID)
			if err != nil {
				return err
			}
			if status != nil {
				remaining = append(remaining, agentID)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workloads still active: %s", strings.Join(remaining, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *Server) purgeGuildData(ctx context.Context, model *store.GuildModel) error {
	if s.msgClient != nil {
		if err := s.msgClient.DeleteNamespace(ctx, model.ID); err != nil {
			return fmt.Errorf("messaging cleanup failed")
		}
	}
	if s.fileStore != nil {
		if err := s.fileStore.DeleteGuildPrefix(ctx, filesystem.DependencyConfig{Protocol: "file"}, model.OrganizationID, model.ID); err != nil {
			return fmt.Errorf("canonical filesystem cleanup failed")
		}
		for _, cfg := range guildFilesystemConfigs(model) {
			if err := s.fileStore.DeleteGuildPrefix(ctx, cfg, model.OrganizationID, model.ID); err != nil {
				return fmt.Errorf("filesystem cleanup failed")
			}
		}
	}
	if err := deleteCanonicalGuildState(model.OrganizationID, model.ID); err != nil {
		return fmt.Errorf("state cleanup failed")
	}
	if err := deleteManagedAgentRuntime(s.dataDir, model.OrganizationID, model.ID); err != nil {
		return fmt.Errorf("agent runtime cleanup failed")
	}
	if err := s.store.PurgeGuild(model); err != nil {
		return fmt.Errorf("database cleanup failed")
	}
	return nil
}

func deleteManagedAgentRuntime(dataDir, orgID, guildID string) error {
	root := filepath.Clean(filepath.Join(dataDir, "agents"))
	target := filepath.Clean(filepath.Join(root, orgID, guildID))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("unsafe managed agent runtime path")
	}
	return os.RemoveAll(target)
}

func guildFilesystemConfigs(model *store.GuildModel) []filesystem.DependencyConfig {
	configs := make([]filesystem.DependencyConfig, 0)
	seen := make(map[string]struct{})
	add := func(dependencies map[string]protocol.DependencySpec) {
		dependency, ok := dependencies["filesystem"]
		if !ok {
			return
		}
		cfg := filesystemConfigFromDependency(dependency)
		key := fmt.Sprintf("%s\x00%s\x00%v", cfg.Protocol, cfg.PathBase, cfg.StorageOptions)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		configs = append(configs, cfg)
	}
	spec := store.ToGuildSpec(model)
	add(spec.DependencyMap)
	for _, agent := range spec.Agents {
		add(agent.DependencyMap)
	}
	return configs
}

func deleteCanonicalGuildState(orgID, guildID string) error {
	root := filepath.Clean(filepath.Join(forgepath.ForgeHome(), "state_stores"))
	target := filepath.Clean(filepath.Join(root, orgID, guildID))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("unsafe canonical state path")
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return nil
}
