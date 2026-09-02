package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/stretchr/testify/require"
)

type deletingStopPusher struct {
	status supervisor.AgentStatusStore
	order  []string
}

func (p *deletingStopPusher) Push(ctx context.Context, _ string, payload []byte) error {
	var wrapper struct {
		Command string               `json:"command"`
		Payload protocol.StopRequest `json:"payload"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return err
	}
	p.order = append(p.order, wrapper.Payload.AgentID)
	return p.status.DeleteStatus(ctx, wrapper.Payload.GuildID, wrapper.Payload.AgentID)
}

func TestDeleteGuildCreatorPurgesQuiescentGuildAndIsIdempotent(t *testing.T) {
	srv, _, db, _, cleanup := setupTestServer(t)
	defer cleanup()
	dataDir := t.TempDir()
	srv.WithDataDir(dataDir)
	model := &store.GuildModel{ID: "guild-delete-1", Name: "Delete", Description: "d", OrganizationID: "org-1", CreatedBy: "user-1", BackendConfig: store.JSONB{}, DependencyMap: store.JSONB{}, Status: store.GuildStatusStopped}
	require.NoError(t, db.CreateGuild(model))
	require.NoError(t, db.AddUserToGuild(model.ID, "user-1"))
	require.NoError(t, srv.msgClient.PublishMessage(context.Background(), model.ID, "topic", &protocol.Message{ID: 1, Payload: json.RawMessage(`{"ok":true}`)}))
	guildRuntime := filepath.Join(dataDir, "agents", model.OrganizationID, model.ID, "agent-1", ".local", "share")
	siblingRuntime := filepath.Join(dataDir, "agents", model.OrganizationID, "guild-delete-sibling", "agent-1")
	require.NoError(t, os.MkdirAll(guildRuntime, 0o755))
	require.NoError(t, os.MkdirAll(siblingRuntime, 0o755))
	defaultFilesystem := filesystem.DependencyConfig{Protocol: "file"}
	require.NoError(t, srv.fileStore.Upload(context.Background(), defaultFilesystem, model.OrganizationID, model.ID, "", "memory.txt", []byte("guild"), "text/plain", nil))
	require.NoError(t, srv.fileStore.Upload(context.Background(), defaultFilesystem, model.OrganizationID, "guild-delete-sibling", "", "memory.txt", []byte("sibling"), "text/plain", nil))
	guildWorkspace := filepath.Dir(srv.fileStore.Resolver().ResolvePath(model.OrganizationID, model.ID, ""))
	siblingWorkspace := filepath.Dir(srv.fileStore.Resolver().ResolvePath(model.OrganizationID, "guild-delete-sibling", ""))

	request := httptest.NewRequest(http.MethodDelete, "/api/guilds/"+model.ID+"?user_id=user-1&org_id=org-1", nil)
	request.SetPathValue("id", model.ID)
	response := httptest.NewRecorder()
	srv.HandleDeleteGuild(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	_, err := db.GetGuild(model.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	messages, err := srv.msgClient.GetMessagesForTopic(context.Background(), model.ID, "topic")
	require.NoError(t, err)
	require.Empty(t, messages)
	_, err = os.Stat(filepath.Join(dataDir, "agents", model.OrganizationID, model.ID))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(siblingRuntime)
	require.NoError(t, err)
	_, err = os.Stat(guildWorkspace)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(siblingWorkspace)
	require.NoError(t, err)

	second := httptest.NewRecorder()
	srv.HandleDeleteGuild(second, request)
	require.Equal(t, http.StatusNoContent, second.Code)
}

func TestDeleteGuildEnforcesCreatorOrganizationAndQuiescence(t *testing.T) {
	srv, _, db, _, cleanup := setupTestServer(t)
	defer cleanup()
	model := &store.GuildModel{ID: "guild-delete-live", Name: "Delete", Description: "d", OrganizationID: "org-1", CreatedBy: "user-1", BackendConfig: store.JSONB{}, DependencyMap: store.JSONB{}, Status: store.GuildStatusRunning}
	require.NoError(t, db.CreateGuild(model))

	for _, query := range []string{"user_id=user-2&org_id=org-1", "user_id=user-1&org_id=org-2"} {
		req := httptest.NewRequest(http.MethodDelete, "/?"+query, nil)
		req.SetPathValue("id", model.ID)
		resp := httptest.NewRecorder()
		srv.HandleDeleteGuild(resp, req)
		require.Equal(t, http.StatusForbidden, resp.Code)
	}

	require.NoError(t, srv.statusStore.WriteStatus(context.Background(), model.ID, model.ID+"#manager_agent", &supervisor.AgentStatusJSON{State: "running", Timestamp: time.Now()}, time.Minute))
	req := httptest.NewRequest(http.MethodDelete, "/?user_id=user-1&org_id=org-1", bytes.NewReader(nil))
	req.SetPathValue("id", model.ID)
	resp := httptest.NewRecorder()
	srv.HandleDeleteGuild(resp, req)
	require.Equal(t, http.StatusConflict, resp.Code)
	var body guildDeletionError
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	require.Equal(t, "guild_not_quiescent", body.Code)
	_, err := db.GetGuild(model.ID)
	require.NoError(t, err)
}

func TestDeleteGuildUnavailableInHostedModeAndRejectsUnsafeID(t *testing.T) {
	t.Setenv("FORGE_IDENTITY_MODE", "hosted")
	srv, _, _, _, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodDelete, "/?user_id=u&org_id=o", nil)
	req.SetPathValue("id", "guild-1")
	resp := httptest.NewRecorder()
	srv.HandleDeleteGuild(resp, req)
	require.Equal(t, http.StatusNotImplemented, resp.Code)

	t.Setenv("FORGE_IDENTITY_MODE", "local")
	req.SetPathValue("id", "../unsafe")
	resp = httptest.NewRecorder()
	srv.HandleDeleteGuild(resp, req)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
}

func TestForceDeleteStopsManagerBeforeChildren(t *testing.T) {
	srv, _, db, _, cleanup := setupTestServer(t)
	defer cleanup()
	model := &store.GuildModel{ID: "guild-force-1", Name: "Force", Description: "d", OrganizationID: "org-1", CreatedBy: "user-1", BackendConfig: store.JSONB{}, DependencyMap: store.JSONB{}, Status: store.GuildStatusRunning}
	require.NoError(t, db.CreateGuild(model))
	for _, agentID := range []string{"agent-1", "agent-2"} {
		require.NoError(t, db.CreateAgent(&store.AgentModel{ID: agentID, GuildID: &model.ID, Name: agentID, ClassName: "test.Agent"}))
		require.NoError(t, srv.statusStore.WriteStatus(context.Background(), model.ID, agentID, &supervisor.AgentStatusJSON{State: "running"}, time.Minute))
	}
	managerID := model.ID + "#manager_agent"
	require.NoError(t, srv.statusStore.WriteStatus(context.Background(), model.ID, managerID, &supervisor.AgentStatusJSON{State: "running"}, time.Minute))
	pusher := &deletingStopPusher{status: srv.statusStore}
	srv.controlPusher = pusher

	req := httptest.NewRequest(http.MethodDelete, "/?user_id=user-1&org_id=org-1&force=true", nil)
	req.SetPathValue("id", model.ID)
	resp := httptest.NewRecorder()
	srv.HandleDeleteGuild(resp, req)
	require.Equal(t, http.StatusNoContent, resp.Code, resp.Body.String())
	require.Equal(t, managerID, pusher.order[0])
	require.ElementsMatch(t, []string{"agent-1", "agent-2"}, pusher.order[1:])
}
