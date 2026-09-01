package api

import (
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

type EnsureGuildRequest struct {
	GuildSpec      *protocol.GuildSpec `json:"guild_spec"`
	OrganizationID string              `json:"organization_id"`
}

type EnsureGuildResponse struct {
	GuildSpec  *protocol.GuildSpec `json:"guild_spec"`
	WasCreated bool                `json:"was_created"`
	Status     store.GuildStatus   `json:"status"`
}

type GuildSpecWithStatusResponse struct {
	GuildSpec *protocol.GuildSpec `json:"guild_spec"`
	Status    store.GuildStatus   `json:"status"`
}

type UpdateGuildStatusRequest struct {
	Status store.GuildStatus `json:"status"`
}

type UpdateGuildStatusResponse struct {
	GuildID string            `json:"guild_id"`
	Status  store.GuildStatus `json:"status"`
}

type EnsureAgentResponse struct {
	AgentID string `json:"agent_id"`
	Created bool   `json:"created"`
}

type EnsureAgentRequest struct {
	Version            string              `json:"version"`
	AgentSpec          *protocol.AgentSpec `json:"agent_spec"`
	DependencyProfiles []string            `json:"dependency_profiles"`
}

type UpdateAgentStatusRequest struct {
	Status store.AgentStatus `json:"status"`
}

type UpdateAgentStatusResponse struct {
	AgentID string            `json:"agent_id"`
	Status  store.AgentStatus `json:"status"`
	Found   bool              `json:"found"`
}

type AddRouteRequest struct {
	RoutingRule *protocol.RoutingRule `json:"routing_rule"`
}

type AddRouteResponse struct {
	RuleHashID string `json:"rule_hashid"`
}

type RemoveRouteResponse struct {
	Deleted bool `json:"deleted"`
}

type HeartbeatStatusUpdateRequest struct {
	AgentID     string            `json:"agent_id"`
	AgentStatus store.AgentStatus `json:"agent_status"`
	GuildStatus store.GuildStatus `json:"guild_status"`
}

type HeartbeatStatusUpdateResponse struct {
	AgentID     string            `json:"agent_id"`
	AgentStatus store.AgentStatus `json:"agent_status"`
	GuildStatus store.GuildStatus `json:"guild_status"`
	AgentFound  bool              `json:"agent_found"`
}
