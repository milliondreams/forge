package leader

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
)

func setupTestNodes(t *testing.T, count int, startPort int) []*RaftElector {
	var electors []*RaftElector
	var baseGossip = startPort
	var baseRaft = startPort + count

	var joinPeers []string

	for i := 0; i < count; i++ {
		nodeID := fmt.Sprintf("node-%d", i)
		raftAddr := fmt.Sprintf("127.0.0.1:%d", baseRaft+i)
		gossipAddr := fmt.Sprintf("127.0.0.1:%d", baseGossip+i)

		cfg := RaftConfig{
			NodeID:          nodeID,
			RaftBindAddr:    raftAddr,
			GossipBindAddr:  gossipAddr,
			GossipJoinPeers: joinPeers, // First node has empty join peers
		}

		e, err := NewRaftElector(cfg)
		if err != nil {
			t.Fatalf("failed to create RaftElector %d: %v", i, err)
		}

		electors = append(electors, e)

		// Add to join peers so subsequent nodes can join the cluster via gossip
		if len(joinPeers) == 0 {
			joinPeers = append(joinPeers, gossipAddr)
		}
	}

	return electors
}

func TestRaftElector_QuorumFormation(t *testing.T) {
	// Spin up 3 nodes starting at port 8600
	electors := setupTestNodes(t, 3, 8600)
	defer func() {
		for _, e := range electors {
			e.Close()
		}
	}()

	// Wait for the cluster to elect a leader
	// This tests the Gossip Notification -> raft.AddVoter logic
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wait for all nodes to agree on a leader or at least one to claim leadership
	leaderCount := 0
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for leader election across 3 nodes")
		case <-time.After(1 * time.Second):
			leaderCount = 0
			for _, e := range electors {
				if e.IsLeader() {
					leaderCount++
				}
			}
			if leaderCount == 1 {
				// Success, we have exactly one leader!
				return
			}
			if leaderCount > 1 {
				t.Fatalf("split brain detected, %d leaders", leaderCount)
			}
		}
	}
}

func TestRaftElector_Failover(t *testing.T) {
	// Spin up 3 nodes starting at port 8700
	electors := setupTestNodes(t, 3, 8700)
	leaderIdx := -1
	leaderClosed := false
	t.Cleanup(func() {
		for i, e := range electors {
			if leaderClosed && i == leaderIdx {
				continue
			}
			e.Close()
		}
	})

	// Do not kill the initial seed leader until all three nodes are committed
	// as voters. Leadership can appear before gossip reconciliation finishes;
	// killing the sole voter in that window leaves no quorum for failover.
	require.Eventually(t, func() bool {
		leaderIdx = -1
		for i, e := range electors {
			if e.IsLeader() {
				if leaderIdx != -1 {
					return false
				}
				leaderIdx = i
			}
		}
		if leaderIdx == -1 {
			return false
		}

		future := electors[leaderIdx].raftNode.GetConfiguration()
		if future.Error() != nil {
			return false
		}
		voterCount := 0
		for _, server := range future.Configuration().Servers {
			if server.Suffrage == raft.Voter {
				voterCount++
			}
		}
		return voterCount == len(electors)
	}, 20*time.Second, 100*time.Millisecond, "cluster did not commit all voters")

	// Kill the leader
	electors[leaderIdx].Close()
	leaderClosed = true

	// Wait for a follower to take over
	require.Eventually(t, func() bool {
		for i, e := range electors {
			if i != leaderIdx && e.IsLeader() {
				return true
			}
		}
		return false
	}, 20*time.Second, 100*time.Millisecond, "timeout waiting for failover")
}
