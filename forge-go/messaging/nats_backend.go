package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const defaultNATSMessageTTL = 3600 * time.Second

// longRetentionTTL is used for topics that need extended message history (e.g. user notifications).
const longRetentionTTL = 60 * 24 * time.Hour // 60 days

// longRetentionTopics lists topic substrings that should use extended retention.
var longRetentionTopics = []string{
	"user_notifications:",
	"user_message_broadcast",
}

// NATSConfig holds configuration for the NATS backend.
type NATSConfig struct {
	// MessageTTL controls retention for JetStream streams and KV buckets.
	// Reads RUSTIC_AI_NATS_MSG_TTL env var (matching Python), defaults to 3600s.
	MessageTTL time.Duration
}

// defaultNATSConfig builds configuration from environment variables.
func defaultNATSConfig() NATSConfig {
	ttl := defaultNATSMessageTTL

	if val := os.Getenv("RUSTIC_AI_NATS_MSG_TTL"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			ttl = time.Duration(parsed) * time.Second
		} else {
			slog.Warn("Invalid RUSTIC_AI_NATS_MSG_TTL, falling back to default", "val", val, "default", defaultNATSMessageTTL.Seconds())
		}
	}

	return NATSConfig{MessageTTL: ttl}
}

// NATSBackend implements Backend using NATS JetStream for durable storage and
// core NATS pub/sub for live delivery, mirroring the Python NATSMessagingBackend.
type NATSBackend struct {
	nc        *nats.Conn
	js        nats.JetStreamContext
	config    NATSConfig
	mu        sync.Mutex
	streams   map[string]bool          // lazy cache of created streams
	kvBuckets map[string]nats.KeyValue // lazy cache of KV buckets
}

// Compile-time check that NATSBackend satisfies the Backend interface.
var _ Backend = (*NATSBackend)(nil)

// NewNATSBackend creates a NATSBackend with default configuration.
func NewNATSBackend(nc *nats.Conn) (*NATSBackend, error) {
	return NewNATSBackendWithConfig(nc, defaultNATSConfig())
}

// NewNATSBackendWithConfig creates a NATSBackend with explicit configuration.
func NewNATSBackendWithConfig(nc *nats.Conn, config NATSConfig) (*NATSBackend, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("messaging: failed to obtain JetStream context: %w", err)
	}
	return &NATSBackend{
		nc:        nc,
		js:        js,
		config:    config,
		streams:   make(map[string]bool),
		kvBuckets: make(map[string]nats.KeyValue),
	}, nil
}

// ttlForTopic returns the appropriate TTL for a namespaced topic.
// Topics matching longRetentionTopics get extended retention; all others use the default.
func (b *NATSBackend) ttlForTopic(nsTopic string) time.Duration {
	for _, pattern := range longRetentionTopics {
		if strings.Contains(nsTopic, pattern) {
			return longRetentionTTL
		}
	}
	return b.config.MessageTTL
}

// ensureStream lazily creates (or verifies) the JetStream stream for a namespaced topic.
func (b *NATSBackend) ensureStream(nsTopic string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.streams[nsTopic] {
		return nil
	}

	cfg := &nats.StreamConfig{
		Name:     streamName(nsTopic),
		Subjects: []string{jsSubject(nsTopic)},
		MaxAge:   b.ttlForTopic(nsTopic),
	}

	_, err := b.js.AddStream(cfg)
	if err != nil {
		// Stream may already exist on the server (restart scenario) — verify it's accessible.
		if _, lookupErr := b.js.StreamInfo(streamName(nsTopic)); lookupErr == nil {
			b.streams[nsTopic] = true
			return nil
		}
		return fmt.Errorf("failed to ensure stream for %q: %w", nsTopic, err)
	}

	b.streams[nsTopic] = true
	return nil
}

// ensureKV lazily creates (or retrieves) the KV bucket for a namespace.
func (b *NATSBackend) ensureKV(namespace string) (nats.KeyValue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if kv, ok := b.kvBuckets[namespace]; ok {
		return kv, nil
	}

	bucket := kvBucketName(namespace)
	kv, err := b.js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: bucket,
		TTL:    b.config.MessageTTL,
	})
	if err != nil {
		// Bucket may already exist — try to bind.
		kv, err = b.js.KeyValue(bucket)
		if err != nil {
			return nil, fmt.Errorf("failed to ensure KV bucket %q: %w", bucket, err)
		}
	}

	b.kvBuckets[namespace] = kv
	return kv, nil
}

// PublishMessage stores a message durably in JetStream, caches it in a KV bucket for
// O(1) ID lookup, and publishes it via core NATS pub/sub for live listeners.
// Naming conventions match the Python NATSMessagingBackend exactly.
func (b *NATSBackend) PublishMessage(_ context.Context, namespace, topic string, msg *protocol.Message) error {
	bare := topic
	msg.TopicPublishedTo = &bare

	nsTopic := namespace + ":" + topic

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	if err := b.ensureStream(nsTopic); err != nil {
		return err
	}

	kv, err := b.ensureKV(namespace)
	if err != nil {
		return err
	}

	// 1. JetStream for ordered, durable topic storage.
	if _, err := b.js.Publish(jsSubject(nsTopic), msgBytes); err != nil {
		return fmt.Errorf("failed to publish to JetStream subject %q: %w", jsSubject(nsTopic), err)
	}

	// 2. KV for O(1) ID lookup.
	if _, err := kv.Put(strconv.FormatUint(msg.ID, 10), msgBytes); err != nil {
		return fmt.Errorf("failed to put message %d in KV: %w", msg.ID, err)
	}

	// 3. Core NATS pub/sub for live listeners (tier-1, matching Python).
	if err := b.nc.Publish(nsTopic, msgBytes); err != nil {
		return fmt.Errorf("failed to publish message %d to core pub/sub topic %q: %w", msg.ID, nsTopic, err)
	}

	return nil
}

// GetMessagesForTopic retrieves all historical messages from the JetStream stream for a topic.
func (b *NATSBackend) GetMessagesForTopic(_ context.Context, namespace, topic string) ([]protocol.Message, error) {
	nsTopic := namespace + ":" + topic

	if err := b.ensureStream(nsTopic); err != nil {
		return nil, err
	}

	info, err := b.js.StreamInfo(streamName(nsTopic))
	if err != nil {
		return nil, fmt.Errorf("failed to get stream info for %q: %w", nsTopic, err)
	}

	total := info.State.Msgs
	if total == 0 {
		return nil, nil
	}

	sub, err := b.js.PullSubscribe(
		jsSubject(nsTopic),
		"", // ephemeral consumer
		nats.DeliverAll(),
		nats.BindStream(streamName(nsTopic)),
		nats.InactiveThreshold(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull subscription for %q: %w", nsTopic, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	natsMsgs, err := sub.Fetch(int(total), nats.MaxWait(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch messages from stream %q: %w", streamName(nsTopic), err)
	}

	raw := make([]string, 0, len(natsMsgs))
	for _, m := range natsMsgs {
		raw = append(raw, string(m.Data))
		_ = m.Ack()
	}

	return parseAndSortMessages(raw)
}

// GetMessagesSince retrieves messages published after the sinceID message, using
// its embedded timestamp as a JetStream start-time hint and filtering by timestamp
// in memory. Gemstone IDs sort by priority before timestamp, so raw ID comparison
// would drop newer messages with a higher priority.
func (b *NATSBackend) GetMessagesSince(_ context.Context, namespace, topic string, sinceID uint64) ([]protocol.Message, error) {
	nsTopic := namespace + ":" + topic

	if err := b.ensureStream(nsTopic); err != nil {
		return nil, err
	}

	gemstone, err := protocol.ParseGemstoneID(sinceID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sinceID %d: %w", sinceID, err)
	}

	startTime := time.UnixMilli(gemstone.Timestamp)

	sub, err := b.js.PullSubscribe(
		jsSubject(nsTopic),
		"", // ephemeral consumer
		nats.StartTime(startTime),
		nats.BindStream(streamName(nsTopic)),
		nats.InactiveThreshold(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull subscription for %q: %w", nsTopic, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	const batchSize = 256
	var messages []protocol.Message

	for {
		natsMsgs, fetchErr := sub.Fetch(batchSize, nats.MaxWait(200*time.Millisecond))
		if fetchErr == nats.ErrTimeout {
			break
		}
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to fetch messages from stream %q: %w", streamName(nsTopic), fetchErr)
		}

		for _, m := range natsMsgs {
			var msg protocol.Message
			if jsonErr := json.Unmarshal(m.Data, &msg); jsonErr == nil {
				msgGemstone, parseErr := protocol.ParseGemstoneID(msg.ID)
				if parseErr != nil {
					_ = m.Ack()
					return nil, fmt.Errorf("failed to parse message ID %d: %w", msg.ID, parseErr)
				}
				if msgGemstone.Timestamp > gemstone.Timestamp {
					messages = append(messages, msg)
				}
			}
			_ = m.Ack()
		}

		if len(natsMsgs) < batchSize {
			break
		}
	}

	sort.Slice(messages, func(i, j int) bool {
		gi, _ := protocol.ParseGemstoneID(messages[i].ID)
		gj, _ := protocol.ParseGemstoneID(messages[j].ID)
		return protocol.Compare(gi, gj) < 0
	})

	return messages, nil
}

// GetMessagesByID bulk-fetches messages from the KV cache by their IDs.
func (b *NATSBackend) GetMessagesByID(_ context.Context, namespace string, msgIDs []uint64) ([]protocol.Message, error) {
	if len(msgIDs) == 0 {
		return nil, nil
	}

	kv, err := b.ensureKV(namespace)
	if err != nil {
		return nil, err
	}

	var messages []protocol.Message
	for _, id := range msgIDs {
		entry, err := kv.Get(strconv.FormatUint(id, 10))
		if err == nats.ErrKeyNotFound {
			continue // missing message, skip
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get message %d from KV: %w", id, err)
		}

		var msg protocol.Message
		if jsonErr := json.Unmarshal(entry.Value(), &msg); jsonErr == nil {
			messages = append(messages, msg)
		}
	}

	return messages, nil
}

// Close drains and closes the NATS connection.
func (b *NATSBackend) Close() error {
	return b.nc.Drain()
}
