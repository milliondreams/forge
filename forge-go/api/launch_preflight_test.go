package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/stretchr/testify/require"
)

func TestLaunchPreflightIsMandatoryOpaqueAndRemediable(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	require.NoError(t, os.WriteFile(registryPath, []byte(`entries:
  - id: SERPAgent
    class_name: test.SERPAgent
    runtime: uvx
    requirements:
      secrets:
        - key: SERP_API_KEY
          env: SERP_API_KEY
          label: SERP API Key
`), 0o600))
	t.Setenv("FORGE_AGENT_REGISTRY", registryPath)
	dependencyPath := filepath.Join(dir, "dependencies.yaml")
	require.NoError(t, os.WriteFile(dependencyPath, []byte("{}\n"), 0o600))
	t.Setenv("FORGE_DEPENDENCY_CONFIG", dependencyPath)
	reg, err := registry.Load(registryPath, nil)
	require.NoError(t, err)

	db, err := store.NewGormStore("sqlite", filepath.Join(dir, "forge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "test.SERPAgent", AgentName: "SERPAgent",
		AgentPropsSchema: store.JSONB{"type": "object"}, MessageHandlers: store.JSONB{},
	}))
	blueprint, err := db.CreateBlueprint(&store.Blueprint{
		Name: "Research", Description: "test", Exposure: store.ExposurePublic, AuthorID: "author",
		Spec: store.JSONB{
			"name": "Research", "description": "test", "properties": map[string]interface{}{},
			"agents": []interface{}{map[string]interface{}{
				"id": "search", "name": "Search", "description": "search", "class_name": "test.SERPAgent",
			}},
		},
	})
	require.NoError(t, err)

	secretStore := secrets.NewInMemorySecretStore()
	server := &Server{
		store: db, credentialRegistry: reg,
		secretManager: secrets.NewManager(secretStore), launchPreflights: newLaunchPreflightCache(),
	}
	guildID := "research-1"
	launchRequest := LaunchGuildFromBlueprintRequest{
		GuildID: &guildID, GuildName: "Research 1", UserID: "user-1", OrgID: "org-1", Configuration: map[string]interface{}{},
	}

	directBody, _ := json.Marshal(launchRequest)
	directRequest := httptest.NewRequest(http.MethodPost, "/catalog/blueprints/"+blueprint.ID+"/guilds", bytes.NewReader(directBody))
	directRequest.SetPathValue("id", blueprint.ID)
	directRecorder := httptest.NewRecorder()
	handleLaunchGuildFromBlueprint(server)(directRecorder, directRequest)
	require.Equal(t, http.StatusUnprocessableEntity, directRecorder.Code)

	preflight := performPreflightRequest(t, server, blueprint.ID, launchRequest)
	require.False(t, preflight.Ready)
	require.Len(t, preflight.Requirements, 1)
	require.Equal(t, "missing", preflight.Requirements[0].Status)
	encoded, _ := json.Marshal(preflight)
	require.NotContains(t, string(encoded), "SERP_API_KEY")
	require.NotContains(t, string(encoded), "env")

	actionBody, _ := json.Marshal(launchSecretActionRequest{Value: base64.StdEncoding.EncodeToString([]byte("serp-secret"))})
	actionRequest := httptest.NewRequest(http.MethodPost, preflight.Requirements[0].Action.Href, bytes.NewReader(actionBody))
	actionRequest.SetPathValue("preflight_id", preflight.ID)
	actionRequest.SetPathValue("requirement_id", preflight.Requirements[0].ID)
	actionRequest.Header.Set("X-Forge-Effective-User", "user-1")
	actionRequest.Header.Set("X-Forge-Effective-Org", "org-1")
	actionRecorder := httptest.NewRecorder()
	server.handleLaunchSecretAction()(actionRecorder, actionRequest)
	require.Equal(t, http.StatusOK, actionRecorder.Code)
	require.True(t, secretStore.Exists("org-1", "SERP_API_KEY"))

	ready := performPreflightRequest(t, server, blueprint.ID, launchRequest)
	require.True(t, ready.Ready)
	launchRequest.PreflightID, launchRequest.Fingerprint = ready.ID, ready.Fingerprint
	launchBody, _ := json.Marshal(launchRequest)
	launchHTTP := httptest.NewRequest(http.MethodPost, "/catalog/blueprints/"+blueprint.ID+"/guilds", bytes.NewReader(launchBody))
	launchHTTP.SetPathValue("id", blueprint.ID)
	launchRecorder := httptest.NewRecorder()
	handleLaunchGuildFromBlueprint(server)(launchRecorder, launchHTTP)
	require.Equal(t, http.StatusCreated, launchRecorder.Code, launchRecorder.Body.String())
}

func TestLaunchRejectsExpiredPreflightWithCurrentSnapshot(t *testing.T) {
	server, blueprint, request := newSecretlessPreflightFixture(t)
	preflight := performPreflightRequest(t, server, blueprint.ID, request)
	require.True(t, preflight.Ready)
	server.launchPreflights.mu.Lock()
	record := server.launchPreflights.records[preflight.ID]
	record.ExpiresAt = time.Now().Add(-time.Second)
	server.launchPreflights.records[preflight.ID] = record
	server.launchPreflights.mu.Unlock()

	request.PreflightID, request.Fingerprint = preflight.ID, preflight.Fingerprint
	body, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost, "/catalog/blueprints/"+blueprint.ID+"/guilds", bytes.NewReader(body))
	httpRequest.SetPathValue("id", blueprint.ID)
	recorder := httptest.NewRecorder()
	handleLaunchGuildFromBlueprint(server)(recorder, httpRequest)
	require.Equal(t, http.StatusPreconditionFailed, recorder.Code)
	var current LaunchPreflightResponse
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&current))
	require.True(t, current.Ready)
	require.NotEqual(t, preflight.ID, current.ID)
}

func TestLaunchRejectsPreflightForAnotherIdentity(t *testing.T) {
	server, blueprint, request := newSecretlessPreflightFixture(t)
	preflight := performPreflightRequest(t, server, blueprint.ID, request)
	require.True(t, preflight.Ready)

	request.UserID = "another-user"
	request.PreflightID, request.Fingerprint = preflight.ID, preflight.Fingerprint
	body, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost, "/catalog/blueprints/"+blueprint.ID+"/guilds", bytes.NewReader(body))
	httpRequest.SetPathValue("id", blueprint.ID)
	recorder := httptest.NewRecorder()
	handleLaunchGuildFromBlueprint(server)(recorder, httpRequest)
	require.Equal(t, http.StatusPreconditionFailed, recorder.Code)
}

func TestLaunchPreflightCacheIsBounded(t *testing.T) {
	server := &Server{launchPreflights: newLaunchPreflightCache()}
	for i := 0; i < maxLaunchPreflightRecords+7; i++ {
		response := LaunchPreflightResponse{Fingerprint: "fingerprint", Ready: true}
		server.rememberLaunchPreflight(&response, "user", "org", "blueprint")
	}
	server.launchPreflights.mu.Lock()
	defer server.launchPreflights.mu.Unlock()
	require.Len(t, server.launchPreflights.records, maxLaunchPreflightRecords)
}

func performPreflightRequest(t *testing.T, server *Server, blueprintID string, request LaunchGuildFromBlueprintRequest) LaunchPreflightResponse {
	t.Helper()
	body, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost, "/catalog/blueprints/"+blueprintID+"/guilds/preflight", bytes.NewReader(body))
	httpRequest.SetPathValue("blueprint_id", blueprintID)
	recorder := httptest.NewRecorder()
	server.handlePreflightGuildFromBlueprint()(recorder, httpRequest)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response LaunchPreflightResponse
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	return response
}

func newSecretlessPreflightFixture(t *testing.T) (*Server, *store.Blueprint, LaunchGuildFromBlueprintRequest) {
	t.Helper()
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	require.NoError(t, os.WriteFile(registryPath, []byte(`entries:
  - id: Agent
    class_name: test.Agent
    runtime: binary
    executable: test-agent
`), 0o600))
	dependencyPath := filepath.Join(dir, "dependencies.yaml")
	require.NoError(t, os.WriteFile(dependencyPath, []byte("{}\n"), 0o600))
	t.Setenv("FORGE_AGENT_REGISTRY", registryPath)
	t.Setenv("FORGE_DEPENDENCY_CONFIG", dependencyPath)
	reg, err := registry.Load(registryPath, nil)
	require.NoError(t, err)
	db, err := store.NewGormStore("sqlite", filepath.Join(dir, "forge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "test.Agent", AgentName: "Agent", AgentPropsSchema: store.JSONB{"type": "object"}, MessageHandlers: store.JSONB{},
	}))
	blueprint, err := db.CreateBlueprint(&store.Blueprint{
		Name: "Blueprint", Description: "test", Exposure: store.ExposurePublic, AuthorID: "author",
		Spec: store.JSONB{"name": "Blueprint", "description": "test", "agents": []interface{}{map[string]interface{}{
			"id": "agent", "name": "Agent", "description": "test", "class_name": "test.Agent",
		}}},
	})
	require.NoError(t, err)
	server := &Server{store: db, credentialRegistry: reg, launchPreflights: newLaunchPreflightCache()}
	guildID := "guild-" + strings.ReplaceAll(t.Name(), "/", "-")
	return server, blueprint, LaunchGuildFromBlueprintRequest{
		GuildID: &guildID, GuildName: "Guild", UserID: "user", OrgID: "org", Configuration: map[string]interface{}{},
	}
}
