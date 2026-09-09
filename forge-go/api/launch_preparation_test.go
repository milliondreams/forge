package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
)

type preparationControlStub struct {
	mu       sync.Mutex
	requests []protocol.PrepareRuntimeRequest
	results  map[string][]byte
}

func (s *preparationControlStub) Push(_ context.Context, _ string, payload []byte) error {
	var wrapper control.ControlMessageWrapper
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return err
	}
	var request protocol.PrepareRuntimeRequest
	if err := json.Unmarshal(wrapper.Payload, &request); err != nil {
		return err
	}
	response, _ := json.Marshal(protocol.PrepareRuntimeResponse{
		RequestID: request.RequestID,
		Success:   true,
		NodeID:    "node-1",
		Cached:    request.Kind == protocol.PrepareRuntimeAgentEnvironment,
	})
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.results[request.RequestID] = response
	s.mu.Unlock()
	return nil
}

func (s *preparationControlStub) Pop(context.Context, string, time.Duration) ([]byte, error) {
	return nil, nil
}

func (s *preparationControlStub) QueueDepth(context.Context, string) (int64, error) {
	return 0, nil
}

func (s *preparationControlStub) PushResponse(context.Context, string, []byte, time.Duration) error {
	return nil
}

func (s *preparationControlStub) WaitResponse(_ context.Context, requestID string, _ time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results[requestID], nil
}

func TestLaunchPreparationDeduplicatesEnvironmentsAndPinsAgents(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "registry.yaml")
	registryYAML := "entries:\n" +
		"  - id: manager\n    class_name: " + guild.GuildManagerClassName + "\n    runtime: uvx\n    package: rusticai-forge\n" +
		"  - id: worker\n    class_name: test.Worker\n    runtime: uvx\n    package: test-worker\n"
	if err := os.WriteFile(registryPath, []byte(registryYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_AGENT_REGISTRY", registryPath)

	previousRegistry := scheduler.GlobalNodeRegistry
	previousScheduler := scheduler.GlobalScheduler
	previousPlacements := scheduler.GlobalPreparedPlacementMap
	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalScheduler = scheduler.NewScheduler(scheduler.GlobalNodeRegistry)
	scheduler.GlobalPreparedPlacementMap = scheduler.NewPreparedPlacementMap()
	t.Cleanup(func() {
		scheduler.GlobalNodeRegistry = previousRegistry
		scheduler.GlobalScheduler = previousScheduler
		scheduler.GlobalPreparedPlacementMap = previousPlacements
	})
	scheduler.GlobalNodeRegistry.RegisterWithCapabilities(
		"node-1",
		scheduler.ResourceCapacity{CPUs: 8, Memory: 8192},
		nil,
		[]string{protocol.RuntimePreparationV1Capability},
	)

	stub := &preparationControlStub{results: map[string][]byte{}}
	server := (&Server{}).WithLaunchPreparationControl(stub)
	guildID := "guild-prepared"
	request := LaunchGuildFromBlueprintRequest{
		GuildID: &guildID, GuildName: "Prepared", UserID: "user", OrgID: "org",
		PreflightID: "preflight", Fingerprint: "fingerprint",
	}
	spec := &protocol.GuildSpec{
		ID: guildID, Name: "Prepared",
		Agents: []protocol.AgentSpec{
			{ID: "worker-1", ClassName: "test.Worker"},
			{ID: "worker-2", ClassName: "test.Worker"},
		},
	}
	record, created, err := server.createPreparationRecord("blueprint", request, spec)
	if err != nil || !created {
		t.Fatalf("create preparation: created=%v err=%v", created, err)
	}
	deadline := time.Now().Add(time.Second)
	for server.launchPreparationSnapshot(record).Status != "ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := server.launchPreparationSnapshot(record)
	if snapshot.Status != "ready" || snapshot.TotalUnits != 3 || snapshot.CompletedUnits != 3 || snapshot.CachedUnits != 2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	stub.mu.Lock()
	requestCount := len(stub.requests)
	stub.mu.Unlock()
	if requestCount != 3 {
		t.Fatalf("control requests = %d, want python plus two unique environments", requestCount)
	}
	for _, agentID := range []string{guildID + "#manager_agent", "worker-1", "worker-2"} {
		placement, ok := scheduler.GlobalPreparedPlacementMap.Find(guildID, agentID)
		if !ok || placement.NodeID != "node-1" {
			t.Fatalf("prepared placement for %s = %#v, %v", agentID, placement, ok)
		}
	}

	request.PreparationID = record.response.ID
	scheduler.GlobalPreparedPlacementMap.RemovePreparation(record.response.ID)
	if prepared, code := server.validateLaunchPreparation(record.response.ID, request, "blueprint", "fingerprint"); prepared != nil || code != "prepared_node_unavailable" {
		t.Fatalf("missing placement validation = %#v, %q", prepared, code)
	}
}

func TestLaunchPreparationIgnoresLateCompletionAfterCancellation(t *testing.T) {
	server := &Server{launchPreparations: newLaunchPreparationStore()}
	record := &launchPreparationRecord{
		response: LaunchPreparationResponse{
			ID: "prep-canceled", Fingerprint: "fingerprint", Status: "canceled", Phase: "complete",
			ExpiresAt: time.Now().UTC().Add(time.Minute),
			Error:     &LaunchPreparationError{Code: "preparation_canceled", Message: "Launch preparation was canceled."},
		},
		userID: "user", orgID: "org", blueprintID: "blueprint", guildID: "guild",
	}
	server.launchPreparations.records[record.response.ID] = record
	guildID := "guild"
	request := LaunchGuildFromBlueprintRequest{
		GuildID: &guildID, UserID: "user", OrgID: "org", Fingerprint: "fingerprint", PreparationID: record.response.ID,
	}
	if prepared, code := server.validateLaunchPreparation(record.response.ID, request, "blueprint", "fingerprint"); prepared != nil || code != "preparation_canceled" {
		t.Fatalf("canceled preparation validation = %#v, %q", prepared, code)
	}

	server.updatePreparation(record, func(response *LaunchPreparationResponse) {
		response.Status = "ready"
		response.Phase = "complete"
	})

	snapshot := server.launchPreparationSnapshot(record)
	if snapshot.Status != "canceled" {
		t.Fatalf("late completion changed canceled preparation to %q", snapshot.Status)
	}
}
