package messaging_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// startInProcessNATSServer launches an in-process JetStream-enabled NATS server
// on a random port and registers a test-cleanup shutdown.
func startInProcessNATSServer(t *testing.T) *natsserver.Server {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1, // random available port
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "failed to create in-process NATS server")

	go s.Start()

	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("in-process NATS server did not become ready within 5s")
	}

	t.Cleanup(func() { s.Shutdown() })
	return s
}

func TestNATSPublishAndRetrieveMessages(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	ctx := context.Background()

	gen, err := protocol.NewGemstoneGenerator(1)
	require.NoError(t, err)

	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	id2, err := gen.Generate(protocol.PriorityUrgent) // lower numeric priority value → sorts first
	require.NoError(t, err)

	ns := "test_ns"
	topic := "my_topic"

	user1 := "user_1"
	sys := "system"
	msg1 := &protocol.Message{ID: id1.ToInt(), Sender: protocol.AgentTag{Name: &user1}}
	msg2 := &protocol.Message{ID: id2.ToInt(), Sender: protocol.AgentTag{Name: &sys}}

	require.NoError(t, backend.PublishMessage(ctx, ns, topic, msg1))
	require.NoError(t, backend.PublishMessage(ctx, ns, topic, msg2))

	// GetMessagesForTopic — sorted by Gemstone priority (Urgent < Normal).
	messages, err := backend.GetMessagesForTopic(ctx, ns, topic)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, msg2.ID, messages[0].ID, "Urgent should sort first")
	assert.Equal(t, msg1.ID, messages[1].ID)

	// GetMessagesByID — KV lookup path.
	byID, err := backend.GetMessagesByID(ctx, ns, []uint64{id1.ToInt(), id2.ToInt()})
	require.NoError(t, err)
	require.Len(t, byID, 2)
	// KV get preserves input order.
	assert.Equal(t, msg1.ID, byID[0].ID)
	assert.Equal(t, msg2.ID, byID[1].ID)
}

func TestNATSGetMessagesSince(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	ctx := context.Background()

	gen, err := protocol.NewGemstoneGenerator(1)
	require.NoError(t, err)

	topic := "paginated_topic"
	ns := "ns"

	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	id2, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	id3, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)

	for _, id := range []protocol.GemstoneID{id1, id2, id3} {
		require.NoError(t, backend.PublishMessage(ctx, ns, topic, &protocol.Message{ID: id.ToInt()}))
	}

	// Fetch since id1 — expect id2, id3.
	msgs, err := backend.GetMessagesSince(ctx, ns, topic, id1.ToInt())
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, id2.ToInt(), msgs[0].ID)
	assert.Equal(t, id3.ToInt(), msgs[1].ID)

	// Fetch since id2 — expect id3 only.
	msgs2, err := backend.GetMessagesSince(ctx, ns, topic, id2.ToInt())
	require.NoError(t, err)
	require.Len(t, msgs2, 1)
	assert.Equal(t, id3.ToInt(), msgs2[0].ID)
}

func TestNATSDefaultTopicPriorityBoundaryParity(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	ctx := context.Background()
	const (
		namespace = "guild-test"
		topic     = "default_topic"
	)
	baseTimestamp := protocol.EPOCH + 1_000_000

	boundary, err := protocol.NewGemstoneID(protocol.PriorityNormal, baseTimestamp, 1, 0)
	require.NoError(t, err)
	sameMillisecond, err := protocol.NewGemstoneID(protocol.PriorityNormal, baseTimestamp, 1, 1)
	require.NoError(t, err)
	laterNormal, err := protocol.NewGemstoneID(protocol.PriorityNormal, baseTimestamp+1, 1, 0)
	require.NoError(t, err)
	laterHigh, err := protocol.NewGemstoneID(protocol.PriorityHigh, baseTimestamp+2, 1, 0)
	require.NoError(t, err)
	laterUrgent, err := protocol.NewGemstoneID(protocol.PriorityUrgent, baseTimestamp+3, 1, 0)
	require.NoError(t, err)

	require.Less(t, laterHigh.ToInt(), boundary.ToInt(), "higher-priority newer ID must reproduce numeric inversion")
	require.Less(t, laterUrgent.ToInt(), boundary.ToInt(), "urgent newer ID must reproduce numeric inversion")

	fixtures := []struct {
		label string
		id    protocol.GemstoneID
	}{
		{label: "boundary", id: boundary},
		{label: "same_millisecond", id: sameMillisecond},
		{label: "later_normal", id: laterNormal},
		{label: "later_high", id: laterHigh},
		{label: "later_urgent", id: laterUrgent},
	}
	for _, fixture := range fixtures {
		msg := protocol.NewMessageFromGemstoneID(fixture.id)
		msg.Topics = protocol.TopicsFromString(topic)
		msg.Payload = json.RawMessage(`{"label":"` + fixture.label + `"}`)
		require.NoError(t, backend.PublishMessage(ctx, namespace, topic, &msg))
	}

	otherGuildMsg := protocol.NewMessageFromGemstoneID(laterNormal)
	require.NoError(t, backend.PublishMessage(ctx, "guild-other", topic, &otherGuildMsg))

	messages, err := backend.GetMessagesSince(ctx, namespace, topic, boundary.ToInt())
	require.NoError(t, err)

	actualIDs := make([]uint64, 0, len(messages))
	actualDetails := make([]string, 0, len(messages))
	for _, msg := range messages {
		actualIDs = append(actualIDs, msg.ID)
		require.NotNil(t, msg.TopicPublishedTo)
		assert.Equal(t, topic, *msg.TopicPublishedTo)

		var payload struct {
			Label string `json:"label"`
		}
		require.NoError(t, json.Unmarshal(msg.Payload, &payload))
		gemstone, parseErr := protocol.ParseGemstoneID(msg.ID)
		require.NoError(t, parseErr)
		actualDetails = append(actualDetails, fmt.Sprintf(
			"%s(priority=%d,timestamp=%d,id=%d)",
			payload.Label, gemstone.Priority, gemstone.Timestamp, msg.ID,
		))
	}
	expectedIDs := []uint64{laterUrgent.ToInt(), laterHigh.ToInt(), laterNormal.ToInt()}
	assert.Equalf(t, expectedIDs, actualIDs,
		"cursor=%d(priority=%d,timestamp=%d), returned=%v",
		boundary.ToInt(), boundary.Priority, boundary.Timestamp, actualDetails)

	history, err := backend.GetMessagesForTopic(ctx, namespace, topic)
	require.NoError(t, err)
	require.Len(t, history, len(fixtures), "guild namespace must isolate default_topic history")

	byID, err := backend.GetMessagesByID(ctx, namespace, []uint64{
		laterHigh.ToInt(),
		999999999,
		boundary.ToInt(),
	})
	require.NoError(t, err)
	require.Len(t, byID, 2)
	assert.Equal(t, laterHigh.ToInt(), byID[0].ID)
	assert.Equal(t, boundary.ToInt(), byID[1].ID)
}

func TestNATSGetMessagesByID(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	ctx := context.Background()

	gen, err := protocol.NewGemstoneGenerator(2)
	require.NoError(t, err)

	ns := "kv_ns"
	topic := "kv_topic"

	id1, _ := gen.Generate(protocol.PriorityNormal)
	id2, _ := gen.Generate(protocol.PriorityNormal)
	id3, _ := gen.Generate(protocol.PriorityNormal)

	for _, id := range []protocol.GemstoneID{id1, id2, id3} {
		require.NoError(t, backend.PublishMessage(ctx, ns, topic, &protocol.Message{ID: id.ToInt()}))
	}

	// Fetch a subset by ID — should skip the missing ID without error.
	nonExistent := uint64(999999999)
	result, err := backend.GetMessagesByID(ctx, ns, []uint64{id1.ToInt(), nonExistent, id3.ToInt()})
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, id1.ToInt(), result[0].ID)
	assert.Equal(t, id3.ToInt(), result[1].ID)
}

func TestNATSNamingConvention(t *testing.T) {
	// Verify that Go naming helpers produce the same output as the Python equivalents.
	cases := []struct {
		input   string
		wantSan string
		wantJS  string
		wantStr string
	}{
		{
			input:   "guild1:default_topic",
			wantSan: "guild1_default_topic",
			wantJS:  "persist.guild1_default_topic",
			wantStr: "MSGS_guild1_default_topic",
		},
		{
			input:   "ns.org:my.topic$1",
			wantSan: "ns_org_my_topic_1",
			wantJS:  "persist.ns_org_my_topic_1",
			wantStr: "MSGS_ns_org_my_topic_1",
		},
	}

	// Access package-level helpers via the exported test shim (see naming_export_test.go).
	for _, tc := range cases {
		assert.Equal(t, tc.wantSan, messaging.SanitizeForTest(tc.input), "sanitize(%q)", tc.input)
		assert.Equal(t, tc.wantJS, messaging.JsSubjectForTest(tc.input), "jsSubject(%q)", tc.input)
		assert.Equal(t, tc.wantStr, messaging.StreamNameForTest(tc.input), "streamName(%q)", tc.input)
	}

	// kvBucketName
	assert.Equal(t, "msg-cache-test_ns", messaging.KvBucketNameForTest("test_ns"))
	assert.Equal(t, "msg-cache-org_guild", messaging.KvBucketNameForTest("org:guild"))
}
