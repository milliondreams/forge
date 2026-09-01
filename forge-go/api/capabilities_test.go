package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestForgeCapabilitiesExposeCredentialPreflightFloorOnly(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/rustic/capabilities", nil)

	handleGetRusticCapabilities()(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response RusticCapabilitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Version != "1" {
		t.Fatalf("version = %q, want 1", response.Version)
	}
	want := []string{"launch_requirements_v2", "dependency_profiles_v1"}
	if !reflect.DeepEqual(response.Capabilities, want) {
		t.Fatalf("capabilities = %#v, want %#v", response.Capabilities, want)
	}
}
