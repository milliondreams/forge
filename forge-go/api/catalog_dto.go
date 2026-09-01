package api

import (
	"time"

	"github.com/rustic-ai/forge/forge-go/guild/store"
)

type BlueprintCreateRequest struct {
	Name           string                  `json:"name"`
	Description    string                  `json:"description"`
	Exposure       store.BlueprintExposure `json:"exposure"`
	AuthorID       string                  `json:"author_id"`
	OrganizationID *string                 `json:"organization_id"`
	CategoryID     *string                 `json:"category_id"`
	Version        string                  `json:"version"`
	Icon           *string                 `json:"icon"`
	IntroMsg       *string                 `json:"intro_msg"`
	Spec           store.JSONB             `json:"spec"`
	Tags           []string                `json:"tags,omitempty"`
	Commands       []string                `json:"commands,omitempty"`
	StarterPrompts []string                `json:"starter_prompts,omitempty"`
	AgentIcons     map[string]string       `json:"agent_icons,omitempty"`
}

type BlueprintInfoResponse struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Description    string                  `json:"description"`
	Version        string                  `json:"version"`
	Exposure       store.BlueprintExposure `json:"exposure"`
	AuthorID       string                  `json:"author_id"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
	Icon           *string                 `json:"icon"`
	OrganizationID *string                 `json:"organization_id"`
	CategoryID     *string                 `json:"category_id"`
	CategoryName   *string                 `json:"category_name"`
}

type AccessibleBlueprintResponse struct {
	BlueprintInfoResponse
	AccessOrganizationID *string `json:"access_organization_id"`
}

type BlueprintDetailsResponse struct {
	BlueprintInfoResponse
	Spec           store.JSONB `json:"spec"`
	Tags           []string    `json:"tags"`
	Commands       []string    `json:"commands"`
	StarterPrompts []string    `json:"starter_prompts"`
	IntroMsg       *string     `json:"intro_msg"`
}

type BlueprintCategoryResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BlueprintCategoryCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type TagResponse struct {
	ID        int       `json:"id"`
	Tag       string    `json:"tag"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BlueprintReviewResponse struct {
	ID          string    `json:"id"`
	BlueprintID string    `json:"blueprint_id"`
	UserID      string    `json:"user_id"`
	Rating      int       `json:"rating"`
	Review      *string   `json:"review"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BlueprintReviewsResponse struct {
	Reviews       []BlueprintReviewResponse `json:"reviews"`
	AverageRating float64                   `json:"average_rating"`
	TotalReviews  int                       `json:"total_reviews"`
}

type BlueprintReviewCreateRequest struct {
	Rating int     `json:"rating"`
	Review *string `json:"review"`
	UserID string  `json:"user_id"`
}

type LaunchGuildFromBlueprintRequest struct {
	GuildID       *string                `json:"guild_id"`
	GuildName     string                 `json:"guild_name"`
	UserID        string                 `json:"user_id"`
	OrgID         string                 `json:"org_id"`
	Description   *string                `json:"description"`
	Configuration map[string]interface{} `json:"configuration"`
	PreflightID   string                 `json:"preflight_id"`
	Fingerprint   string                 `json:"fingerprint"`
}

type AgentEntryResponse struct {
	QualifiedClassName string                 `json:"qualified_class_name"`
	AgentName          string                 `json:"agent_name"`
	AgentDoc           *string                `json:"agent_doc"`
	AgentPropsSchema   map[string]interface{} `json:"agent_props_schema"`
	MessageHandlers    map[string]interface{} `json:"message_handlers"`
	AgentDependencies  []AgentDependencyEntry `json:"agent_dependencies"`
}

type AgentNameWithIcon struct {
	AgentName string `json:"agent_name"`
	Icon      string `json:"icon"`
}

type BlueprintAgentsIconReqRes struct {
	AgentIcons []AgentNameWithIcon `json:"agent_icons"`
}
