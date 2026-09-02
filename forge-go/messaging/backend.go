package messaging

import (
	"context"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

// Backend is the messaging abstraction that all backends (Redis, NATS, …) must satisfy.
type Backend interface {
	PublishMessage(ctx context.Context, namespace, topic string, msg *protocol.Message) error
	GetMessagesForTopic(ctx context.Context, namespace, topic string) ([]protocol.Message, error)
	GetMessagesSince(ctx context.Context, namespace, topic string, sinceID uint64) ([]protocol.Message, error)
	GetMessagesByID(ctx context.Context, namespace string, msgIDs []uint64) ([]protocol.Message, error)
	Subscribe(ctx context.Context, namespace string, topics ...string) (Subscription, error)
	DeleteNamespace(ctx context.Context, namespace string) error
	Close() error
}

// Subscription is the live-delivery abstraction returned by Backend.Subscribe.
type Subscription interface {
	Channel() <-chan SubMessage
	ErrChannel() <-chan error
	Close() error
}
