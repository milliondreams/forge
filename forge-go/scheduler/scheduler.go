package scheduler

import (
	"fmt"
	"time"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type Scheduler struct {
	registry *NodeRegistry
}

func NewScheduler(reg *NodeRegistry) *Scheduler {
	return &Scheduler{
		registry: reg,
	}
}

func (s *Scheduler) Schedule(agentSpec protocol.AgentSpec) (string, error) {
	start := time.Now()
	defer func() {
		telemetry.ObserveSchedulerPlacementDuration(time.Since(start))
	}()

	var reqCPUs, reqMem, reqGPUs int

	if agentSpec.Resources.NumCPUs != nil {
		reqCPUs = int(*agentSpec.Resources.NumCPUs)
	}
	if agentSpec.Resources.NumGPUs != nil {
		reqGPUs = int(*agentSpec.Resources.NumGPUs)
	}
	if agentSpec.Resources.CustomResources != nil {
		if mem, ok := agentSpec.Resources.CustomResources["memory"].(float64); ok {
			reqMem = int(mem)
		}
	}

	nodes := s.registry.ListHealthy()
	if len(nodes) == 0 {
		telemetry.AddSchedulerPlacementError()
		return "", fmt.Errorf("no healthy nodes available in the cluster")
	}

	var bestNode string
	bestFitScore := -1
	requiredProfiles := dependencyProfiles(agentSpec)

	for _, n := range nodes {
		if !nodeReadyFor(&n, requiredProfiles) {
			continue
		}
		remCPUs := n.TotalCapacity.CPUs - n.UsedCapacity.CPUs
		remMem := n.TotalCapacity.Memory - n.UsedCapacity.Memory
		remGPUs := n.TotalCapacity.GPUs - n.UsedCapacity.GPUs

		if remCPUs >= reqCPUs && remMem >= reqMem && remGPUs >= reqGPUs {
			score := remMem + (remCPUs * 1024)

			if bestNode == "" || score > bestFitScore {
				bestFitScore = score
				bestNode = n.NodeID
			}
		}
	}

	if bestNode == "" {
		telemetry.AddSchedulerPlacementError()
		return "", fmt.Errorf("no node with sufficient capacity [%d cpus, %d mem, %d gpus]", reqCPUs, reqMem, reqGPUs)
	}

	s.registry.AllocateCapacity(bestNode, ResourceCapacity{
		CPUs:   reqCPUs,
		Memory: reqMem,
		GPUs:   reqGPUs,
	})

	return bestNode, nil
}

func dependencyProfiles(agentSpec protocol.AgentSpec) []string {
	raw := agentSpec.Properties[protocol.DependencyProfilesProperty]
	result := make([]string, 0)
	switch values := raw.(type) {
	case []string:
		result = append(result, values...)
	case []interface{}:
		for _, value := range values {
			if key, ok := value.(string); ok {
				result = append(result, key)
			}
		}
	}
	return normalizedProfileKeys(result)
}

func (r *NodeRegistry) AllocateCapacity(nodeID string, cap ResourceCapacity) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, exists := r.nodes[nodeID]; exists {
		state.UsedCapacity.CPUs += cap.CPUs
		state.UsedCapacity.Memory += cap.Memory
		state.UsedCapacity.GPUs += cap.GPUs
	}
	r.recordMetricsLocked()
}

func (r *NodeRegistry) DeallocateCapacity(nodeID string, cap ResourceCapacity) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, exists := r.nodes[nodeID]; exists {
		state.UsedCapacity.CPUs = max(0, state.UsedCapacity.CPUs-cap.CPUs)
		state.UsedCapacity.Memory = max(0, state.UsedCapacity.Memory-cap.Memory)
		state.UsedCapacity.GPUs = max(0, state.UsedCapacity.GPUs-cap.GPUs)
	}
	r.recordMetricsLocked()
}
