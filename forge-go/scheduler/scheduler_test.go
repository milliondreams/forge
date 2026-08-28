package scheduler

import (
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func floatPtr(v float64) *float64 {
	return &v
}

func TestScheduler_BestFit(t *testing.T) {
	r := NewNodeRegistry()
	r.Register("node-small", ResourceCapacity{CPUs: 2, Memory: 2048, GPUs: 0})
	r.Register("node-medium", ResourceCapacity{CPUs: 4, Memory: 8192, GPUs: 0})
	r.Register("node-large", ResourceCapacity{CPUs: 8, Memory: 16384, GPUs: 1})

	s := NewScheduler(r)

	// Test 1: Schedule an agent demanding 3 CPUs -> Should pick node-medium or node-large
	// Since node-large has more free capacity, and we use a load spreading formula `remMem + remCPU*1024`, node-large is best fit immediately.
	agent1 := protocol.AgentSpec{
		ID: "agent-1",
		Resources: protocol.ResourceSpec{
			NumCPUs: floatPtr(3),
		},
	}

	nodeID, err := s.Schedule(agent1)
	if err != nil {
		t.Fatalf("Failed to schedule agent1: %v", err)
	}

	if nodeID != "node-large" {
		t.Fatalf("Expected node-large, got %s", nodeID)
	}

	// Test 2: Ensure capacity decrements happen inline
	r.mu.RLock()
	usedLarge := r.nodes["node-large"].UsedCapacity
	r.mu.RUnlock()

	if usedLarge.CPUs != 3 {
		t.Fatalf("Expected node-large to have 3 CPUs used, got %d", usedLarge.CPUs)
	}

	// Test 3: GPU demanded
	agent2 := protocol.AgentSpec{
		ID: "agent-2",
		Resources: protocol.ResourceSpec{
			NumGPUs: floatPtr(1),
		},
	}

	nodeID2, err := s.Schedule(agent2)
	if err != nil {
		t.Fatalf("Failed to schedule agent2 (GPU demand): %v", err)
	}

	if nodeID2 != "node-large" {
		t.Fatalf("Expected agent2 to schedule on node-large due to GPU requirement, got %s", nodeID2)
	}

	// Test 4: Insufficient capacities
	agent3 := protocol.AgentSpec{
		ID: "agent-huge",
		Resources: protocol.ResourceSpec{
			NumCPUs: floatPtr(32),
		},
	}

	_, err = s.Schedule(agent3)
	if err == nil {
		t.Fatalf("Expected error scheduling agent demanding 32 CPUs but it succeeded")
	}
}

func TestScheduler_RequiresReportedDependencyProfiles(t *testing.T) {
	r := NewNodeRegistry()
	r.RegisterWithReadiness("local-only", ResourceCapacity{CPUs: 4, Memory: 4096}, []string{"llm_local_qwen"})
	r.RegisterWithReadiness("hosted", ResourceCapacity{CPUs: 2, Memory: 2048}, []string{"llm_openai"})

	agent := protocol.NewAgentSpec()
	agent.Properties[protocol.DependencyProfilesProperty] = []string{"llm_openai"}
	nodeID, err := NewScheduler(r).Schedule(agent)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != "hosted" {
		t.Fatalf("expected hosted-ready node, got %q", nodeID)
	}
}
