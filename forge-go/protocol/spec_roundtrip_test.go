package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGuildSpecJSONRoundTrip_FullPayloadParity(t *testing.T) {
	routeTimes := 2
	timeout := 30
	retryCount := 1
	latency := 200
	listenToDefault := false
	actOnlyWhenTagged := true
	processStatus := ProcessStatusCompleted
	priority := 3

	spec := GuildSpec{
		ID:          "dto-parity-guild",
		Name:        "DTO Parity Guild",
		Description: "Round-trip test for JSON DTO parity",
		Properties: map[string]interface{}{
			"execution_engine": "rustic_ai.forge.execution_engine.ForgeExecutionEngine",
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		Configuration: map[string]interface{}{
			"mode": "strict",
		},
		Agents: []AgentSpec{
			{
				ID:                   "dto-parity-guild#a-0",
				Name:                 "Echo Agent",
				Description:          "Echo",
				ClassName:            "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				AdditionalTopics:     []string{"echo_topic"},
				Properties:           map[string]interface{}{"temperature": 0.1},
				ListenToDefaultTopic: &listenToDefault,
				ActOnlyWhenTagged:    &actOnlyWhenTagged,
				Predicates: map[string]RuntimePredicate{
					"can_process": {PredicateType: PredicateJSONata, Expression: strPtr("true")},
				},
				DependencyMap: map[string]DependencySpec{
					"llm": {
						ClassName:    "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver",
						ProvidedType: "rustic_ai.core.llm.LLM",
						Properties:   map[string]interface{}{"model": "gpt-4o-mini"},
					},
				},
				AdditionalDependencies: []string{"forge-python"},
				Resources: ResourceSpec{
					NumCPUs: func(v float64) *float64 { return &v }(1),
					NumGPUs: func(v float64) *float64 { return &v }(0),
					Secrets: []string{"OPENAI_API_KEY"},
					CustomResources: map[string]interface{}{
						"memory_mb": 512,
					},
				},
				QOS: QOSSpec{
					Timeout:    &timeout,
					RetryCount: &retryCount,
					Latency:    &latency,
				},
			},
		},
		DependencyMap: map[string]DependencySpec{
			"kvstore": {
				ClassName:    "rustic_ai.core.guild.agent_ext.depends.kvstore.InMemoryKVStoreResolver",
				ProvidedType: "rustic_ai.core.kvstore.KVStore",
				Properties:   map[string]interface{}{},
			},
		},
		Routes: &RoutingSlip{
			Steps: []RoutingRule{
				{
					AgentType:  strPtr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: strPtr("unwrap_and_forward_message"),
					Destination: &RoutingDestination{
						Topics:        TopicsFromSlice([]string{"echo_topic"}),
						RecipientList: []AgentTag{{Name: strPtr("Echo Agent")}},
						Priority:      &priority,
					},
					RouteTimes:       &routeTimes,
					Transformer:      RawJSON(`{"style":"simple","expression_type":"jsonata","expression":"$.x"}`),
					AgentStateUpdate: RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":null}`),
					GuildStateUpdate: RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":null}`),
					ProcessStatus:    &processStatus,
				},
			},
		},
		Gateway: &GatewayConfig{
			Enabled:       true,
			InputFormats:  []string{"rustic_ai.core.messaging.core.message.Message"},
			OutputFormats: []string{"rustic_ai.openai.protocol.ChatCompletionResponse"},
		},
	}

	originalJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal original spec: %v", err)
	}

	var originalMap map[string]interface{}
	if err := json.Unmarshal(originalJSON, &originalMap); err != nil {
		t.Fatalf("unmarshal original into map: %v", err)
	}

	var parsed GuildSpec
	if err := json.Unmarshal(originalJSON, &parsed); err != nil {
		t.Fatalf("unmarshal into GuildSpec: %v", err)
	}

	roundJSON, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal parsed spec: %v", err)
	}

	var roundMap map[string]interface{}
	if err := json.Unmarshal(roundJSON, &roundMap); err != nil {
		t.Fatalf("unmarshal round-trip into map: %v", err)
	}

	if !reflect.DeepEqual(originalMap, roundMap) {
		t.Fatalf("GuildSpec DTO JSON round-trip changed payload.\noriginal=%s\nroundtrip=%s", string(originalJSON), string(roundJSON))
	}
}

func strPtr(v string) *string { return &v }

func TestAgentNeedsJSONRoundTrip_FullPayloadParity(t *testing.T) {
	optionalTrue := true
	optionalFalse := false
	needs := AgentNeeds{
		ClassName: "rustic_ai.browser.agent.BrowserAgent",
		Needs: NeedsSpec{
			Secrets: []SecretNeed{
				{
					Key:      "OPENAI_API_KEY",
					Env:      "OPENAI_API_KEY",
					Label:    "OpenAI API Key",
					Optional: &optionalTrue,
				},
			},
			OAuth: []OAuthNeed{
				{
					Provider: "google",
					Env:      "GOOGLE_ACCESS_TOKEN",
					Label:    "Google Account",
					Scopes:   []string{"gmail.readonly", "drive.readonly"},
					Optional: &optionalFalse,
				},
			},
			Capabilities: []CapabilityNeed{
				{Type: "network", Label: "External network access"},
				{Type: "filesystem", Label: "Local filesystem access"},
			},
			Network: NetworkNeeds{
				Allow: []string{"api.openai.com", "generativelanguage.googleapis.com"},
			},
			Filesystem: FilesystemNeeds{
				Allow: []FilesystemAccessNeed{
					{Path: "/home/user/code/project", Mode: "rw"},
				},
			},
		},
	}

	originalJSON, err := json.Marshal(needs)
	if err != nil {
		t.Fatalf("marshal original needs: %v", err)
	}

	var originalMap map[string]interface{}
	if err := json.Unmarshal(originalJSON, &originalMap); err != nil {
		t.Fatalf("unmarshal original needs into map: %v", err)
	}

	var parsed AgentNeeds
	if err := json.Unmarshal(originalJSON, &parsed); err != nil {
		t.Fatalf("unmarshal into AgentNeeds: %v", err)
	}

	roundJSON, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal parsed needs: %v", err)
	}

	var roundMap map[string]interface{}
	if err := json.Unmarshal(roundJSON, &roundMap); err != nil {
		t.Fatalf("unmarshal round-trip needs into map: %v", err)
	}

	if !reflect.DeepEqual(originalMap, roundMap) {
		t.Fatalf("AgentNeeds DTO JSON round-trip changed payload.\noriginal=%s\nroundtrip=%s", string(originalJSON), string(roundJSON))
	}
}
