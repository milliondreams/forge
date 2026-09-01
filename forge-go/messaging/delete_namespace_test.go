package messaging

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/require"
)

func TestRedisDeleteNamespaceDoesNotTouchSibling(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	backend := NewRedisBackend(rdb)
	ctx := context.Background()
	for _, namespace := range []string{"guild-a", "guild-a2"} {
		require.NoError(t, backend.PublishMessage(ctx, namespace, "topic", &protocol.Message{ID: 1, Payload: json.RawMessage(`{}`)}))
	}
	require.NoError(t, backend.DeleteNamespace(ctx, "guild-a"))
	deleted, err := backend.GetMessagesForTopic(ctx, "guild-a", "topic")
	require.NoError(t, err)
	require.Empty(t, deleted)
	sibling, err := backend.GetMessagesForTopic(ctx, "guild-a2", "topic")
	require.NoError(t, err)
	require.Len(t, sibling, 1)
}
