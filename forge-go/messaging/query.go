package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// PublishMessage atomically stores a message in the direct-lookup cache, inserts it chronologically
// into the topic's ZSET, and publishes it via PubSub for live listeners.
func (r *RedisBackend) PublishMessage(ctx context.Context, namespace, topic string, msg *protocol.Message) error {
	// Set TopicPublishedTo to the bare topic before namespacing, matching Python's
	// MessagingInterface.publish which sets topic_published_to to the un-namespaced topic.
	bare := topic
	msg.TopicPublishedTo = &bare

	// Namespace the topic for Redis storage/pubsub, matching Python's MessagingInterface
	// which internally prepends {guild_id}: to all topics.
	nsTopic := namespace + ":" + topic

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	gemstone, err := protocol.ParseGemstoneID(msg.ID)
	if err != nil {
		return fmt.Errorf("failed to parse gemstone ID: %w", err)
	}

	cacheKey := fmt.Sprintf("msg:%s:%d", namespace, msg.ID)
	strJSON := string(msgBytes)

	// Pipeline the storage commands
	pipe := r.rdb.Pipeline()

	// 1. Store in the absolute lookup cache
	pipe.Set(ctx, cacheKey, strJSON, r.config.MessageTTL)

	// 2. Add to the chronological ZSET for the topic, using timestamp as the score
	pipe.ZAdd(ctx, nsTopic, redis.Z{
		Score:  float64(gemstone.Timestamp),
		Member: strJSON,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to execute storage pipeline for message %d: %w", msg.ID, err)
	}

	// 3. Publish for real-time subscribers
	err = r.rdb.Publish(ctx, nsTopic, strJSON).Err()
	if err != nil {
		return fmt.Errorf("failed to publish message %d to topic %s: %w", msg.ID, nsTopic, err)
	}

	return nil
}

// GetMessagesForTopic retrieves all historical messages from a given topic's ZSET.
func (r *RedisBackend) GetMessagesForTopic(ctx context.Context, namespace, topic string) ([]protocol.Message, error) {
	nsTopic := namespace + ":" + topic
	rawMessages, err := r.rdb.ZRange(ctx, nsTopic, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch messages for topic %s: %w", nsTopic, err)
	}

	return parseAndSortMessages(rawMessages)
}

// GetMessagesSince retrieves messages added to a topic's ZSET since a given boundary.
func (r *RedisBackend) GetMessagesSince(ctx context.Context, namespace, topic string, sinceID uint64) ([]protocol.Message, error) {
	nsTopic := namespace + ":" + topic
	gemstone, err := protocol.ParseGemstoneID(sinceID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sinceID: %w", err)
	}

	// Add 1 to bound exclusively above the provided timestamp
	minScore := fmt.Sprintf("%f", float64(gemstone.Timestamp+1))

	rawMessages, err := r.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     nsTopic,
		Start:   minScore,
		Stop:    "+inf",
		ByScore: true,
	}).Result()

	if err != nil {
		return nil, fmt.Errorf("failed to fetch messages since ID %d: %w", sinceID, err)
	}

	return parseAndSortMessages(rawMessages)
}

// GetMessagesByID uses pipelined GETs to bulk-fetch messages directly by their ID.
func (r *RedisBackend) GetMessagesByID(ctx context.Context, namespace string, msgIDs []uint64) ([]protocol.Message, error) {
	if len(msgIDs) == 0 {
		return nil, nil
	}

	pipe := r.rdb.Pipeline()
	var cmds []*redis.StringCmd

	for _, id := range msgIDs {
		cacheKey := fmt.Sprintf("msg:%s:%d", namespace, id)
		cmds = append(cmds, pipe.Get(ctx, cacheKey))
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		// redis.Nil means some keys were missing, which we tolerate
		return nil, fmt.Errorf("failed to execute fetch pipeline: %w", err)
	}

	var parsed []protocol.Message
	for _, cmd := range cmds {
		val, err := cmd.Result()
		if err == redis.Nil {
			continue // missing message, skip
		}
		if err != nil {
			return nil, err
		}

		var m protocol.Message
		if err := json.Unmarshal([]byte(val), &m); err == nil {
			parsed = append(parsed, m)
		}
	}

	return parsed, nil
}

// DeleteNamespace removes only durable keys owned by one guild namespace.
// SCAN keeps the operation bounded and avoids blocking Redis on large stores.
func (r *RedisBackend) DeleteNamespace(ctx context.Context, namespace string) error {
	patterns := []string{"msg:" + namespace + ":*", namespace + ":*"}
	for _, pattern := range patterns {
		var cursor uint64
		for {
			keys, next, err := r.rdb.Scan(ctx, cursor, pattern, 256).Result()
			if err != nil {
				return fmt.Errorf("scan messaging namespace: %w", err)
			}
			if len(keys) > 0 {
				if err := r.rdb.Del(ctx, keys...).Err(); err != nil {
					return fmt.Errorf("delete messaging namespace keys: %w", err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}

// parseAndSortMessages is a package-level helper used by both Redis and NATS backends.
func parseAndSortMessages(raw []string) ([]protocol.Message, error) {
	var messages []protocol.Message

	for _, val := range raw {
		var m protocol.Message
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			// In Py we usually skip or crash. Let's return error on corrupted data.
			return nil, fmt.Errorf("corrupted data in ZSET: %w", err)
		}
		messages = append(messages, m)
	}

	// Python MessageStore enforces sorting by ID specifically. We can sort them here.
	sort.Slice(messages, func(i, j int) bool {
		gi, _ := protocol.ParseGemstoneID(messages[i].ID)
		gj, _ := protocol.ParseGemstoneID(messages[j].ID)

		// Compare by Gemstone priority ordering
		return protocol.Compare(gi, gj) < 0
	})

	return messages, nil
}
