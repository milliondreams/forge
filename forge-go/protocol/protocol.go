package protocol

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type JSONB map[string]interface{}

// ControlPusher is a minimal interface for enqueueing control messages.
// Defined here (not in control/) to avoid import cycles: control imports protocol.
type ControlPusher interface {
	Push(ctx context.Context, queueKey string, payload []byte) error
}

type SpawnRequest struct {
	RequestID        string                 `json:"request_id"`
	OrganizationID   string                 `json:"organization_id,omitempty"`
	GuildID          string                 `json:"guild_id"`
	AgentSpec        AgentSpec              `json:"agent_spec"`
	MessagingConfig  *MessagingConfig       `json:"messaging_config,omitempty"`
	MachineID        int                    `json:"machine_id,omitempty"`
	ClientType       string                 `json:"client_type,omitempty"`
	ClientProperties JSONB                  `json:"client_properties,omitempty"`
	Requirements     CredentialRequirements `json:"credential_requirements,omitempty"`
	ResponseMode     string                 `json:"response_mode,omitempty"`
	TraceContext     map[string]string      `json:"trace_context,omitempty"`
}

const (
	SpawnResponseModeStarted = "started"
	SpawnResponseModeNone    = "none"
)

type StopRequest struct {
	RequestID      string `json:"request_id"`
	OrganizationID string `json:"organization_id,omitempty"`
	GuildID        string `json:"guild_id"`
	AgentID        string `json:"agent_id"`
}

type SpawnResponse struct {
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type StopResponse struct {
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
}

type ErrorResponse struct {
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error"`
}

// PushSpawnRequest serialises req and enqueues it via the given ControlPusher.
func PushSpawnRequest(ctx context.Context, pusher ControlPusher, req SpawnRequest) error {
	ctx, span := otel.Tracer("forge.control").Start(ctx, "queue.publish")
	defer span.End()

	if req.TraceContext == nil {
		req.TraceContext = make(map[string]string)
	}
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.MapCarrier(req.TraceContext))

	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}

	data, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("serialize spawn request wrapper: %w", err)
	}

	const queueName = "forge:control:requests"

	if err := pusher.Push(ctx, queueName, data); err != nil {
		telemetry.AddQueueProcessingError(queueName, "spawn", "push_failed")
		return fmt.Errorf("push to %s: %w", queueName, err)
	}

	telemetry.AddQueuePublish(queueName, "spawn")

	return nil
}

// PushStopRequest routes an owned workload stop through Forge's existing
// control plane and supervisor path.
func PushStopRequest(ctx context.Context, pusher ControlPusher, req StopRequest) error {
	wrapper := map[string]interface{}{"command": "stop", "payload": req}
	data, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("serialize stop request wrapper: %w", err)
	}
	const queueName = "forge:control:requests"
	if err := pusher.Push(ctx, queueName, data); err != nil {
		telemetry.AddQueueProcessingError(queueName, "stop", "push_failed")
		return fmt.Errorf("push to %s: %w", queueName, err)
	}
	telemetry.AddQueuePublish(queueName, "stop")
	return nil
}
