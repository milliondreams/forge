package scheduler

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type ResourceCapacity struct {
	CPUs   int `json:"cpus"`
	Memory int `json:"memory"`
	GPUs   int `json:"gpus"`
}

type NodeState struct {
	NodeID                  string           `json:"node_id"`
	TotalCapacity           ResourceCapacity `json:"total_capacity"`
	UsedCapacity            ResourceCapacity `json:"used_capacity"`
	ReadyDependencyProfiles []string         `json:"ready_dependency_profiles"`
	LastHeartbeat           time.Time        `json:"last_heartbeat"`
}

type NodeRegistry struct {
	mu    sync.RWMutex
	nodes map[string]*NodeState
}

func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		nodes: make(map[string]*NodeState),
	}
}

func (r *NodeRegistry) Register(nodeID string, capacity ResourceCapacity) {
	r.RegisterWithReadiness(nodeID, capacity, nil)
}

func (r *NodeRegistry) RegisterWithReadiness(nodeID string, capacity ResourceCapacity, profiles []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, exists := r.nodes[nodeID]; exists {
		state.TotalCapacity = capacity
		state.ReadyDependencyProfiles = normalizedProfileKeys(profiles)
		state.LastHeartbeat = time.Now()
	} else {
		r.nodes[nodeID] = &NodeState{
			NodeID:                  nodeID,
			TotalCapacity:           capacity,
			UsedCapacity:            ResourceCapacity{},
			ReadyDependencyProfiles: normalizedProfileKeys(profiles),
			LastHeartbeat:           time.Now(),
		}
	}
	r.recordMetricsLocked()
}

func (r *NodeRegistry) Heartbeat(nodeID string) bool {
	return r.HeartbeatWithReadiness(nodeID, nil, false)
}

func (r *NodeRegistry) HeartbeatWithReadiness(nodeID string, profiles []string, update bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, exists := r.nodes[nodeID]; exists {
		latency := time.Since(state.LastHeartbeat)
		telemetry.ObserveNodeHeartbeatLatency(nodeID, latency)

		state.LastHeartbeat = time.Now()
		if update {
			state.ReadyDependencyProfiles = normalizedProfileKeys(profiles)
		}
		return true
	}

	return false
}

func normalizedProfileKeys(profiles []string) []string {
	seen := make(map[string]struct{}, len(profiles))
	result := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		if _, exists := seen[profile]; exists {
			continue
		}
		seen[profile] = struct{}{}
		result = append(result, profile)
	}
	sort.Strings(result)
	return result
}

func nodeReadyFor(state *NodeState, required []string) bool {
	ready := make(map[string]struct{}, len(state.ReadyDependencyProfiles))
	for _, key := range state.ReadyDependencyProfiles {
		ready[key] = struct{}{}
	}
	for _, key := range required {
		if _, exists := ready[key]; !exists {
			return false
		}
	}
	return true
}

func (r *NodeRegistry) AnyHealthyNodeReadyFor(required []string) bool {
	if len(required) == 0 {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	for _, state := range r.nodes {
		if now.Sub(state.LastHeartbeat) < 10*time.Second && nodeReadyFor(state, required) {
			return true
		}
	}
	return false
}

func (r *NodeRegistry) Deregister(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.nodes, nodeID)
	r.recordMetricsLocked()
}

// recordMetricsLocked must be called with r.mu held.
func (r *NodeRegistry) recordMetricsLocked() {
	nodesRegistered := len(r.nodes)
	telemetry.SetNodesRegistered(float64(nodesRegistered))

	var availableSlots float64
	for _, state := range r.nodes {
		freeCPU := state.TotalCapacity.CPUs - state.UsedCapacity.CPUs
		if freeCPU > 0 {
			availableSlots += float64(freeCPU)
		}
	}
	telemetry.SetAvailableAgentSlots(availableSlots)
}

func (r *NodeRegistry) IsHealthy(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	state, exists := r.nodes[nodeID]
	if !exists {
		return false
	}

	return time.Since(state.LastHeartbeat) < 10*time.Second
}

func (r *NodeRegistry) ListHealthy() []NodeState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var healthy []NodeState
	now := time.Now()
	for _, state := range r.nodes {
		if now.Sub(state.LastHeartbeat) < 10*time.Second {
			healthy = append(healthy, *state)
		}
	}
	return healthy
}

var GlobalNodeRegistry = NewNodeRegistry()
