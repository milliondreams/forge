package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
)

func TestAPIDTOs_JSONRoundTrip_FullMatrix(t *testing.T) {
	now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	orgID := "acmeorganizationid"
	categoryID := "cat-1"
	icon := "https://example.com/icon.svg"
	intro := "Welcome"
	reviewText := "Looks good"

	routeTimes := 1
	listenToDefaultTopic := true
	actOnlyWhenTagged := false
	spec := &protocol.GuildSpec{
		ID:          "dto-guild-1",
		Name:        "DTO Guild",
		Description: "Guild for API DTO tests",
		Properties: map[string]interface{}{
			"execution_engine": "rustic_ai.forge.execution_engine.ForgeExecutionEngine",
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0}},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:                   "dto-guild-1#a-0",
				Name:                 "Echo Agent",
				Description:          "Echo",
				ClassName:            "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				Properties:           map[string]interface{}{},
				AdditionalTopics:     []string{"echo_topic"},
				ListenToDefaultTopic: &listenToDefaultTopic,
				ActOnlyWhenTagged:    &actOnlyWhenTagged,
			},
		},
		DependencyMap: map[string]protocol.DependencySpec{},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{
				{
					AgentType:  ptrString("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: ptrString("unwrap_and_forward_message"),
					Destination: &protocol.RoutingDestination{
						Topics: protocol.TopicsFromSlice([]string{"echo_topic"}),
					},
					RouteTimes:       &routeTimes,
					Transformer:      protocol.RawJSON(`{"style":"simple","expression_type":"jsonata","output_format":"generic_json","expression":"$.payload"}`),
					AgentStateUpdate: protocol.RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":"{\"seen\":true}"}`),
					GuildStateUpdate: protocol.RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":"{\"runs\":1}"}`),
				},
			},
		},
	}

	cases := []struct {
		name string
		v    interface{}
	}{
		{
			name: "AgentRegistryEntry",
			v: AgentRegistryEntry{
				Name:               "EchoAgent",
				AgentName:          "EchoAgent",
				QualifiedClassName: "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				AgentDoc:           "Echo",
				AgentPropsSchema:   map[string]interface{}{"type": "object"},
				MessageHandlers:    map[string]interface{}{},
				AgentDependencies: []AgentDependencyEntry{
					{
						DependencyKey: "llm",
						AgentLevel:    ptrBool(true),
						VariableName:  ptrString("llm"),
						RequiredType:  ptrString("rustic_ai.core.llm.LLM"),
					},
				},
			},
		},
		{
			name: "NodeRegistrationRequest",
			v: NodeRegistrationRequest{
				NodeID: "node-1",
				Capacity: scheduler.ResourceCapacity{
					CPUs: 8, Memory: 16384, GPUs: 1,
				},
			},
		},
		{
			name: "CreateGuildRequest",
			v: CreateGuildRequest{
				Spec:           spec,
				OrganizationID: "acmeorganizationid",
			},
		},
		{
			name: "createBoardRequest",
			v: createBoardRequest{
				GuildID: "g-1", Name: "General", CreatedBy: "dummyuserid", IsDefault: true, IsPrivate: false,
			},
		},
		{
			name: "addMessageRequest",
			v:    addMessageRequest{MessageID: "12345"},
		},
		{
			name: "BlueprintCreateRequest",
			v: BlueprintCreateRequest{
				Name:           "Simple Echo",
				Description:    "Echo app",
				Exposure:       "public",
				AuthorID:       "dummyuserid",
				OrganizationID: &orgID,
				CategoryID:     &categoryID,
				Version:        "v1",
				Icon:           &icon,
				IntroMsg:       &intro,
				Spec: map[string]interface{}{
					"name":        "Simple Echo",
					"description": "Echo app",
					"routes":      map[string]interface{}{"steps": []map[string]interface{}{{"method_name": "unwrap_and_forward_message"}}},
				},
				Tags:           []string{"echo"},
				Commands:       []string{"/echo"},
				StarterPrompts: []string{"Say hi"},
				AgentIcons:     map[string]string{"Echo Agent": "https://example.com/echo.svg"},
			},
		},
		{
			name: "BlueprintInfoResponse",
			v: BlueprintInfoResponse{
				ID:             "bp-1",
				Name:           "Simple Echo",
				Description:    "Echo app",
				Version:        "v1",
				Exposure:       "public",
				AuthorID:       "dummyuserid",
				CreatedAt:      now,
				UpdatedAt:      now,
				Icon:           &icon,
				OrganizationID: &orgID,
				CategoryID:     &categoryID,
				CategoryName:   ptrString("Utilities"),
			},
		},
		{
			name: "BlueprintDetailsResponse",
			v: BlueprintDetailsResponse{
				BlueprintInfoResponse: BlueprintInfoResponse{
					ID:             "bp-1",
					Name:           "Simple Echo",
					Description:    "Echo app",
					Version:        "v1",
					Exposure:       "public",
					AuthorID:       "dummyuserid",
					CreatedAt:      now,
					UpdatedAt:      now,
					OrganizationID: &orgID,
				},
				Spec:           map[string]interface{}{"name": "Simple Echo"},
				Tags:           []string{"echo"},
				Commands:       []string{"/echo"},
				StarterPrompts: []string{"Say hi"},
				IntroMsg:       &intro,
			},
		},
		{
			name: "BlueprintCategoryResponse",
			v: BlueprintCategoryResponse{
				ID: "cat-1", Name: "Utilities", Description: "Utility tools", CreatedAt: now, UpdatedAt: now,
			},
		},
		{
			name: "BlueprintCategoryCreateRequest",
			v: BlueprintCategoryCreateRequest{
				Name: "Utilities", Description: "Utility tools",
			},
		},
		{
			name: "TagResponse",
			v: TagResponse{
				ID: 1, Tag: "echo", CreatedAt: now, UpdatedAt: now,
			},
		},
		{
			name: "BlueprintReviewResponse",
			v: BlueprintReviewResponse{
				ID: "r-1", BlueprintID: "bp-1", UserID: "dummyuserid", Rating: 5, Review: &reviewText, CreatedAt: now, UpdatedAt: now,
			},
		},
		{
			name: "BlueprintReviewCreateRequest",
			v: BlueprintReviewCreateRequest{
				Rating: 5, Review: &reviewText, UserID: "dummyuserid",
			},
		},
		{
			name: "LaunchGuildFromBlueprintRequest",
			v: LaunchGuildFromBlueprintRequest{
				GuildID:     ptrString("g-1"),
				GuildName:   "test001",
				UserID:      "dummyuserid",
				OrgID:       "acmeorganizationid",
				Description: ptrString("desc"),
				Configuration: map[string]interface{}{
					"temperature": 0.1,
				},
			},
		},
		{
			name: "AgentNameWithIcon",
			v: AgentNameWithIcon{
				AgentName: "Echo Agent",
				Icon:      "https://example.com/echo.svg",
			},
		},
		{
			name: "BlueprintAgentsIconReqRes",
			v: BlueprintAgentsIconReqRes{
				AgentIcons: []AgentNameWithIcon{{AgentName: "Echo Agent", Icon: "https://example.com/echo.svg"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertDTORoundTripJSON(t, tc.v)
		})
	}
}

func assertDTORoundTripJSON(t *testing.T, in interface{}) {
	t.Helper()
	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	typ := reflect.TypeOf(in)
	ptr := reflect.New(typ)
	if err := json.Unmarshal(encoded, ptr.Interface()); err != nil {
		t.Fatalf("unmarshal encoded input: %v", err)
	}

	round, err := json.Marshal(ptr.Elem().Interface())
	if err != nil {
		t.Fatalf("marshal round-tripped value: %v", err)
	}

	assertJSONObjectEqual(t, encoded, round)
}

func assertJSONObjectEqual(t *testing.T, a, b []byte) {
	t.Helper()
	var ma map[string]interface{}
	var mb map[string]interface{}
	if err := json.Unmarshal(a, &ma); err != nil {
		t.Fatalf("unmarshal a map: %v", err)
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		t.Fatalf("unmarshal b map: %v", err)
	}
	if !reflect.DeepEqual(ma, mb) {
		t.Fatalf("json mismatch\na=%s\nb=%s", string(a), string(b))
	}
}

func ptrString(v string) *string { return &v }
func ptrBool(v bool) *bool       { return &v }
