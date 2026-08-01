package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadyzTracksLifecycleAndAgentOSStatus(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, "127.0.0.1:0").
		WithAgentOS(
			1,
			"bwrap",
			"supervisor-zmq",
			time.Second,
			AgentOSPrerequisite{Name: "bubblewrap", Required: true, Satisfied: true, Detail: "available"},
			AgentOSPrerequisite{Name: "local_model_endpoint", Satisfied: true, Detail: "http://10.0.2.101:55262/v1"},
		)
	router := server.buildRouter()

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("starting readyz status = %d, want 503", response.Code)
	}

	server.MarkReady()
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", response.Code)
	}

	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/agentos/v1/status", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("unexpected AgentOS status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	var status AgentOSStatusResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode AgentOS status: %v", err)
	}
	if status.ContractVersion != AgentOSStatusContractVersion || !status.AgentOSMode || !status.Compatible {
		t.Fatalf("unexpected compatibility contract: %+v", status)
	}
	if status.Supervisor != "bwrap" || status.LocalModelBaseURL != "http://10.0.2.101:55262/v1" {
		t.Fatalf("unexpected AgentOS runtime contract: %+v", status)
	}
	if len(status.Prerequisites) != 2 {
		t.Fatalf("prerequisites = %d, want 2", len(status.Prerequisites))
	}
}

func TestAgentOSStatusReportsFailedCompatibilityCheck(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, "127.0.0.1:0").
		WithAgentOS(
			1,
			"bwrap",
			"supervisor-zmq",
			time.Second,
			AgentOSPrerequisite{Name: "bubblewrap", Required: true, Satisfied: false, Detail: "not found"},
		)

	response := httptest.NewRecorder()
	server.buildRouter().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agentos/v1/status", nil))

	var status AgentOSStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode AgentOS status: %v", err)
	}
	if status.Compatible {
		t.Fatalf("failed prerequisite reported compatible: %+v", status)
	}
}

func TestAgentOSCompatibilityAllowsUnavailableOptionalCapability(t *testing.T) {
	prerequisites := []AgentOSPrerequisite{
		{Name: "bubblewrap", Required: true, Satisfied: true},
		{Name: "local_model_endpoint", Required: false, Satisfied: false},
	}
	if !agentOSCompatible(true, prerequisites, nil) {
		t.Fatal("optional local-model capability made AgentOS incompatible")
	}
}

func TestClearAgentOSDependencyCache(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, "127.0.0.1:0").
		WithAgentOS(1, "bwrap", "supervisor-zmq", time.Second)
	called := false
	server.SetDependencyCacheClear(func() error {
		called = true
		return nil
	})
	response := httptest.NewRecorder()
	server.buildRouter().ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/agentos/v1/dependencies/cache", nil))
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("clear cache response = %d, called = %v", response.Code, called)
	}
}

func TestClearAgentOSDependencyCacheIsUnavailableNatively(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, "127.0.0.1:0")
	response := httptest.NewRecorder()
	server.buildRouter().ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/agentos/v1/dependencies/cache", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("native clear cache response = %d, want 404", response.Code)
	}
}
