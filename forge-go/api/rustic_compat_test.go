package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/stretchr/testify/require"
)

func TestTransformRusticMessage_ChatCompletionResponse(t *testing.T) {
	payload := json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`)
	msg := protocol.NewMessage()
	msg.ID = 123
	msg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionResponse"
	msg.Payload = payload
	msg.Priority = int(protocol.PriorityNormal)
	msg.Topics = protocol.TopicsFromString("user_notifications:dummyuserid")
	msg.Thread = []uint64{100, 123}

	req := httptest.NewRequest(http.MethodGet, "http://localhost/rustic/api/guilds/g1/dummyuserid/messages", nil)
	out := transformRusticMessage(msg, "g1", req)

	require.Equal(t, "123", out["id"])
	require.Equal(t, "MarkdownFormat", out["format"])
	data, ok := out["data"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "hello", data["text"])
	require.Equal(t, "NORMAL", out["priority"])
}

func TestTransformRusticMessage_ParticipantListUsesLegacyUIShape(t *testing.T) {
	msg := protocol.NewMessage()
	msg.ID = 124
	msg.Format = "rustic_ai.core.agents.utils.user_proxy_agent.ParticipantList"
	msg.Payload = json.RawMessage(`{"participants":[{"id":"a-1","name":"Echo Agent","type":"bot"}]}`)
	msg.Priority = int(protocol.PriorityImportant)
	msg.Topics = protocol.TopicsFromString("user_system_notification:dummyuserid")

	req := httptest.NewRequest(http.MethodGet, "http://localhost/rustic/api/guilds/g1/dummyuserid/messages", nil)
	out := transformRusticMessage(msg, "g1", req)

	require.Equal(t, "participants", out["format"])
	require.Equal(t, "IMPORTANT", out["priority"])

	data, ok := out["data"].([]interface{})
	require.True(t, ok)
	require.Len(t, data, 1)

	first, ok := data[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "a-1", first["id"])
	require.Equal(t, "Echo Agent", first["name"])
	require.Equal(t, "bot", first["type"])
}

func TestRusticMessagesRoute_ShapesLegacyEnvelope(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	t.Setenv(localUserIDEnvVar, "local-user-123")
	t.Setenv(localUserNameEnvVar, "Alice")

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	msgClient := messaging.NewClient(rdb)
	dbPath := filepath.Join(t.TempDir(), "rustic-compat.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	defer func() { _ = dbStore.Close() }()

	s := NewServer(dbStore, supervisor.NewRedisAgentStatusStore(rdb), control.NewRedisControlTransport(rdb), msgClient, nil, ":0")
	router := s.buildRouter()

	msg := protocol.NewMessage()
	msg.ID = 999
	msg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionResponse"
	msg.Payload = json.RawMessage(`{"choices":[{"message":{"content":"echo"}}]}`)
	msg.Topics = protocol.TopicsFromString("user_notifications:dummyuserid")
	msg.Priority = int(protocol.PriorityNormal)
	msg.Thread = []uint64{999}
	msg.MessageHistory = []protocol.ProcessEntry{}
	msg.Normalize()

	require.NoError(t, msgClient.PublishMessage(context.Background(), "g1", "user_notifications:dummyuserid", &msg))

	req := httptest.NewRequest(http.MethodGet, "/rustic/api/guilds/g1/local-user-123/messages", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body, 1)
	require.Equal(t, "999", body[0]["id"])
	require.Equal(t, "MarkdownFormat", body[0]["format"])
}

func TestRusticFileRoutes_ProxyStyleRewrite(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	t.Setenv("FORGE_RUSTIC_THIS_SERVER", "http://proxy.example:3001/rustic")

	s, _, _, mux, cleanup := setupTestServer(t)
	defer cleanup()
	router := s.buildRouter()

	createReq := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          "g-rustic-files",
			Name:        "Rustic Files",
			Description: "guild for rustic file parity tests",
			Agents:      []protocol.AgentSpec{},
			Properties:  map[string]any{},
		},
		OrganizationID: "org-1",
	}
	createBody, err := json.Marshal(createReq)
	require.NoError(t, err)
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/guilds", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	rrCreate := httptest.NewRecorder()
	mux.ServeHTTP(rrCreate, reqCreate)
	require.Equal(t, http.StatusCreated, rrCreate.Code)

	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	part, err := writer.CreateFormFile("file", "hello.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reqUpload := httptest.NewRequest(http.MethodPost, "/rustic/api/guilds/g-rustic-files/files/", &uploadBody)
	reqUpload.Header.Set("Content-Type", writer.FormDataContentType())
	reqUpload.Host = "localhost:3001"
	rrUpload := httptest.NewRecorder()
	router.ServeHTTP(rrUpload, reqUpload)
	require.Equal(t, http.StatusOK, rrUpload.Code)

	var uploadResp map[string]interface{}
	require.NoError(t, json.Unmarshal(rrUpload.Body.Bytes(), &uploadResp))
	require.Equal(t, "http://proxy.example:3001/rustic/api/guilds/g-rustic-files/files/hello.txt", uploadResp["url"])

	reqList := httptest.NewRequest(http.MethodGet, "/rustic/api/guilds/g-rustic-files/files/", nil)
	reqList.Host = "localhost:3001"
	rrList := httptest.NewRecorder()
	router.ServeHTTP(rrList, reqList)
	require.Equal(t, http.StatusOK, rrList.Code)

	var listResp []map[string]interface{}
	require.NoError(t, json.Unmarshal(rrList.Body.Bytes(), &listResp))
	require.Len(t, listResp, 1)
	require.Equal(t, "hello.txt", listResp[0]["name"])
	require.Equal(t, "http://proxy.example:3001/rustic/api/guilds/g-rustic-files/files/hello.txt", listResp[0]["url"])
}

func TestRusticCatalogAgentDependenciesRoute(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	db, err := store.NewGormStore("sqlite", "file::memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	depType := "rustic_ai.core.llm.LLM"
	varName := "llm"
	agentLevel := true
	require.NoError(t, db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "rustic_ai.llm_agent.llm_agent.LLMAgent",
		AgentName:          "LLMAgent",
		AgentDoc:           ptrString("LLM agent"),
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
		AgentDependencies: store.JSONB{
			"llm": map[string]any{
				"dependency_key": "llm",
				"agent_level":    agentLevel,
				"variable_name":  varName,
				"required_type":  depType,
			},
		},
	}))

	s := NewServer(db, nil, nil, nil, nil, ":0")
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/rustic/catalog/agents/rustic_ai.llm_agent.llm_agent.LLMAgent/dependencies", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var deps []AgentDependencyEntry
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &deps))
	require.Len(t, deps, 1)
	require.Equal(t, "llm", deps[0].DependencyKey)
	require.NotNil(t, deps[0].RequiredType)
	require.Equal(t, depType, *deps[0].RequiredType)
	require.NotNil(t, deps[0].VariableName)
	require.Equal(t, varName, *deps[0].VariableName)
}

func TestRusticConfiguredDependenciesRoutes(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	configPath := filepath.Join(t.TempDir(), "agent-dependencies.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
llm_openai:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: gpt-5.4
filesystem:
  class_name: rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver
  provided_type: rustic_ai.core.filesystem.FileSystem
  properties:
    path_base: /tmp
llm_unavailable:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  catalog:
    display_name: Unavailable LLM
    selectable: true
  requirements:
    secrets: [TEST_MISSING_DEPENDENCY_SECRET]
  properties:
    model: unavailable
llm_hidden:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  catalog:
    display_name: Hidden LLM
    selectable: false
  properties:
    model: hidden
`), 0o600))
	t.Setenv("FORGE_DEPENDENCY_CONFIG", configPath)

	db, err := store.NewGormStore("sqlite", "file::memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	s := NewServer(db, nil, nil, nil, nil, ":0")
	router := s.buildRouter()

	t.Run("list all", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rustic/catalog/dependencies", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)

		var deps []ConfiguredDependencyEntry
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &deps))
		require.Len(t, deps, 2)
		require.Equal(t, "filesystem", deps[0].Key)
		require.Equal(t, "llm_openai", deps[1].Key)
	})

	t.Run("include unavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rustic/catalog/dependencies?include_unavailable=true", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)

		var deps []ConfiguredDependencyEntry
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &deps))
		require.Len(t, deps, 3)
		require.Equal(t, "needs_configuration", deps[2].Availability.Status)
	})

	t.Run("filter by query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rustic/catalog/dependencies?provided_type=rustic_ai.core.llm.LLM", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)

		var deps []ConfiguredDependencyEntry
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &deps))
		require.Len(t, deps, 1)
		require.Equal(t, "llm_openai", deps[0].Key)
		require.Equal(t, "rustic_ai.core.llm.LLM", deps[0].ProvidedType)
	})

	t.Run("filter by path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rustic/dependencies/provided-type/rustic_ai.core.filesystem.FileSystem", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)

		var deps []ConfiguredDependencyEntry
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &deps))
		require.Len(t, deps, 1)
		require.Equal(t, "filesystem", deps[0].Key)
		require.Equal(t, "rustic_ai.core.filesystem.FileSystem", deps[0].ProvidedType)
	})
}

func TestRusticBlueprintDependenciesRoute(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	configPath := filepath.Join(t.TempDir(), "agent-dependencies.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
llm_openai:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: gpt-5.4
llm_gemini:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.llm.LLM
  properties:
    model: gemini/gemini-3.1
`), 0o600))
	t.Setenv("FORGE_DEPENDENCY_CONFIG", configPath)

	db, err := store.NewGormStore("sqlite", "file::memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	depType := "rustic_ai.core.llm.LLM"
	varName := "llm"
	agentLevel := true
	require.NoError(t, db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "rustic_ai.llm_agent.llm_agent.LLMAgent",
		AgentName:          "LLMAgent",
		AgentDoc:           ptrString("LLM agent"),
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
		AgentDependencies: store.JSONB{
			"llm": map[string]any{
				"dependency_key": "llm",
				"agent_level":    agentLevel,
				"variable_name":  varName,
				"required_type":  depType,
			},
		},
	}))

	bp, err := db.CreateBlueprint(&store.Blueprint{
		Name:        "Research App",
		Description: "Blueprint with LLM dependency",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author-1",
		Spec: store.JSONB{
			"name":        "Research App",
			"description": "Blueprint with LLM dependency",
			"agents": []any{
				map[string]any{
					"id":          "research_agent",
					"name":        "Research Agent",
					"description": "Answers questions",
					"class_name":  "rustic_ai.llm_agent.llm_agent.LLMAgent",
				},
			},
		},
	})
	require.NoError(t, err)

	s := NewServer(db, nil, nil, nil, nil, ":0")
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/rustic/catalog/blueprints/"+bp.ID+"/dependencies", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summaries []BlueprintAgentDependencySummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summaries))
	require.Len(t, summaries, 1)
	require.Equal(t, "Research Agent", summaries[0].AgentName)
	require.Equal(t, "rustic_ai.llm_agent.llm_agent.LLMAgent", summaries[0].QualifiedClassName)
	require.Len(t, summaries[0].Dependencies, 1)
	require.Equal(t, "agent:research_agent:llm", summaries[0].Dependencies[0].BindingKey)
	require.Equal(t, "llm", summaries[0].Dependencies[0].DependencyKey)
	require.NotNil(t, summaries[0].Dependencies[0].RequiredType)
	require.Equal(t, depType, *summaries[0].Dependencies[0].RequiredType)
	require.Len(t, summaries[0].Dependencies[0].Providers, 2)
	require.Equal(t, "llm_gemini", summaries[0].Dependencies[0].Providers[0].Key)
	require.Equal(t, "llm_openai", summaries[0].Dependencies[0].Providers[1].Key)

}
