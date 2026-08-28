package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/dependencies"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

func TestHandleSpawnRejectsDependencyPreparationFailure(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg := loadTestRegistry(t, `entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: uvx
    package: test-package
`)
	statusStore := newFakeStatusStore()
	sup := newOrderTrackingSupervisor()
	backend := newRecordingEventBackend()
	publisher, err := infraevents.NewPublisher(backend)
	require.NoError(t, err)
	prewarmer, err := dependencies.NewCoordinator(dependencies.Config{
		Context:   ctx,
		Registry:  reg,
		Publisher: publisher,
		NodeID:    "test-node",
		Workers:   1,
		UVXPath:   "uvx",
		UVVersion: "uvx 1",
		Run: func(context.Context, string, []string, []string) error {
			return errors.New("package index unavailable")
		},
	})
	require.NoError(t, err)
	handler := NewControlQueueHandler(
		NewRedisControlTransport(rdb), reg, secrets.NewEnvSecretProvider(), sup, nil,
		WithStatusStore(statusStore),
		WithNodeID("test-node"),
		WithInfraEventPublisher(publisher),
		WithDependencyPrewarmer(prewarmer),
	)

	req := &protocol.SpawnRequest{
		RequestID: "req-prewarm-failure",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	}
	handler.handleSpawn(ctx, req)

	sup.mu.Lock()
	assert.Empty(t, sup.launched)
	sup.mu.Unlock()
	status, err := statusStore.GetStatus(ctx, "g1", "a1")
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "failed", status.State)

	response, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-prewarm-failure").Result()
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(response[1]), &payload))
	assert.Contains(t, payload["error"], "dependency_preparation_failed")
	assert.Equal(t, []string{
		"agent.spawn.received",
		"dependency.prepare.started",
		"dependency.prepare.failed",
		"agent.spawn.rejected",
	}, loadInfraEventKinds(t, backend, "g1"))
}
