package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const (
	localDummyUserID    = "dummyuserid"
	localDummyOrgID     = "acmeorganizationid"
	localUserIDEnvVar   = "FORGE_LOCAL_USER_ID"
	localUserNameEnvVar = "FORGE_LOCAL_USER_NAME"
	localAnonymousUser  = "Anonymous User"
)

var validLocalUserID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type localUserInfo struct {
	ID       string `json:"id"`
	FullName string `json:"fullName"`
	Email    string `json:"email"`
}

type localOrganizationMembership struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	UserRoles  []string `json:"userRoles"`
	IsDisabled bool     `json:"isDisabled"`
}

type localOrganizationInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsDisabled bool   `json:"isDisabled"`
}

type localOrganizationDetail struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	CreatedAt  int64  `json:"createdAt"`
	UsersCount int    `json:"usersCount"`
	IsDisabled bool   `json:"isDisabled"`
}

type localRoleResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type localLimitResponse struct {
	Allowed   bool       `json:"allowed"`
	Limit     int        `json:"limit"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Note      string     `json:"note,omitempty"`
}

type localQuotaUpdateRequest struct {
	Limit int     `json:"limit"`
	Note  *string `json:"note"`
}

type localQuota struct {
	limit     int
	updatedBy string
	updatedAt *time.Time
	note      string
}

type wsBootstrapSession struct {
	guildID string
	userID  string
	user    string
	expires time.Time
}

type localUIState struct {
	mu sync.RWMutex

	user localUserInfo
	org  localOrganizationInfo
	orgD localOrganizationDetail

	userMembership localOrganizationMembership
	roles          []localRoleResponse

	guildQuota    localQuota
	orgQuota      localQuota
	orgUsersQuota localQuota

	wsSessions map[string]wsBootstrapSession
}

func newLocalUIState() *localUIState {
	userID := strings.TrimSpace(os.Getenv(localUserIDEnvVar))
	if !validLocalUserID.MatchString(userID) {
		userID = localDummyUserID
	}
	userName := strings.TrimSpace(os.Getenv(localUserNameEnvVar))
	if userName == "" {
		userName = localAnonymousUser
	}

	return &localUIState{
		user: localUserInfo{
			ID:       userID,
			FullName: userName,
			Email:    "anonymous@example.com",
		},
		org: localOrganizationInfo{
			ID:         localDummyOrgID,
			Name:       "Acme",
			IsDisabled: false,
		},
		orgD: localOrganizationDetail{
			ID:         localDummyOrgID,
			Name:       "Acme",
			URL:        "http://localhost:3000",
			CreatedAt:  time.Now().UnixMilli(),
			UsersCount: 1,
			IsDisabled: false,
		},
		userMembership: localOrganizationMembership{
			ID:         localDummyOrgID,
			Name:       "Acme",
			URL:        "http://localhost:3000",
			UserRoles:  []string{"member", "admin"},
			IsDisabled: false,
		},
		roles: []localRoleResponse{
			{
				ID:          "default-member-role",
				Name:        "member",
				Description: "Default member role",
			},
		},
		guildQuota: localQuota{
			limit: 25,
		},
		orgQuota: localQuota{
			limit: 2,
		},
		orgUsersQuota: localQuota{
			limit: 2,
		},
		wsSessions: map[string]wsBootstrapSession{},
	}
}

func (s *localUIState) setWSSession(wsID string, session wsBootstrapSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsSessions[wsID] = session
}

func (s *localUIState) getWSSession(wsID string) (wsBootstrapSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.wsSessions[wsID]
	if !ok {
		return wsBootstrapSession{}, false
	}
	if time.Now().After(session.expires) {
		return wsBootstrapSession{}, false
	}
	return session, true
}

func splitCSVParam(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) registerLocalIdentityRoutes(router gin.IRouter) {
	router.GET("/api/users/search", func(c *gin.Context) {
		userIDs := splitCSVParam(c.Query("userIds"))
		if len(userIDs) == 0 {
			c.JSON(http.StatusOK, []localUserInfo{})
			return
		}
		resp := []localUserInfo{}
		for _, id := range userIDs {
			if id == s.localUI.user.ID {
				resp = append(resp, s.localUI.user)
			}
		}
		c.JSON(http.StatusOK, resp)
	})

	router.GET("/api/users/:user_id", func(c *gin.Context) {
		if c.Param("user_id") != s.localUI.user.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, s.localUI.user)
	})

	router.PATCH("/api/users/:user_id", func(c *gin.Context) {
		if c.Param("user_id") != s.localUI.user.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.Status(http.StatusNoContent)
	})

	router.GET("/api/users/:user_id/organizations", func(c *gin.Context) {
		if c.Param("user_id") != s.localUI.user.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, []localOrganizationMembership{s.localUI.userMembership})
	})

	router.GET("/api/users/:user_id/roles", func(c *gin.Context) {
		if c.Param("user_id") != s.localUI.user.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, []string{})
	})

	router.GET("/api/organizations/search", func(c *gin.Context) {
		orgIDs := splitCSVParam(c.Query("orgIds"))
		if len(orgIDs) == 0 {
			c.Status(http.StatusBadRequest)
			return
		}
		resp := []localOrganizationInfo{}
		for _, id := range orgIDs {
			if id == s.localUI.org.ID {
				resp = append(resp, s.localUI.org)
			}
		}
		c.JSON(http.StatusOK, resp)
	})

	router.GET("/api/organizations/:org_id", func(c *gin.Context) {
		if c.Param("org_id") != s.localUI.org.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, s.localUI.orgD)
	})

	router.GET("/api/organizations/:org_id/users", func(c *gin.Context) {
		if c.Param("org_id") != s.localUI.org.ID {
			c.Status(http.StatusNotFound)
			return
		}
		user := gin.H{
			"id":       s.localUI.user.ID,
			"fullName": s.localUI.user.FullName,
			"email":    s.localUI.user.Email,
			"roles": []gin.H{
				{"id": "default-member-role", "name": "member"},
			},
		}
		c.Header("Content-Range", "*/1")
		c.Header("Access-Control-Expose-Headers", "Content-Range")
		c.JSON(http.StatusOK, []gin.H{user})
	})

	router.GET("/api/organizations/:org_id/users/:user_id", func(c *gin.Context) {
		if c.Param("org_id") != s.localUI.org.ID || c.Param("user_id") != s.localUI.user.ID {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":       s.localUI.user.ID,
			"fullName": s.localUI.user.FullName,
			"email":    s.localUI.user.Email,
			"roles": []gin.H{
				{"id": "default-member-role", "name": "member"},
			},
		})
	})

	router.GET("/api/roles", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.localUI.roles)
	})
}

func quotaToResponse(q localQuota) localLimitResponse {
	return localLimitResponse{
		Allowed:   true,
		Limit:     q.limit,
		UpdatedBy: q.updatedBy,
		UpdatedAt: q.updatedAt,
		Note:      q.note,
	}
}

func applyQuotaUpdate(q *localQuota, req localQuotaUpdateRequest, updatedBy string) {
	now := time.Now().UTC()
	if req.Limit > 0 {
		q.limit = req.Limit
	}
	q.updatedBy = updatedBy
	q.updatedAt = &now
	if req.Note != nil {
		q.note = strings.TrimSpace(*req.Note)
	}
}

func (s *Server) registerLocalQuotaRoutes(router gin.IRouter) {
	router.GET("/api/quotas/resources/guilds/check", func(c *gin.Context) {
		if c.Query("orgId") == "" || c.Query("userId") == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "orgId and userId query params are required"})
			return
		}
		s.localUI.mu.RLock()
		resp := quotaToResponse(s.localUI.guildQuota)
		s.localUI.mu.RUnlock()
		c.JSON(http.StatusOK, resp)
	})

	router.GET("/api/quotas/resources/guilds/organizations/:org_id/users/:user_id/check", func(c *gin.Context) {
		s.localUI.mu.RLock()
		resp := quotaToResponse(s.localUI.guildQuota)
		s.localUI.mu.RUnlock()
		c.JSON(http.StatusOK, resp)
	})

	router.PUT("/api/quotas/resources/guilds/organizations/:org_id/users/:user_id", func(c *gin.Context) {
		var req localQuotaUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid json"})
			return
		}
		s.localUI.mu.Lock()
		applyQuotaUpdate(&s.localUI.guildQuota, req, s.localUI.user.ID)
		s.localUI.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"message": "Updated chat quota successfully"})
	})

	router.GET("/api/quotas/resources/guild-users/guilds/:guild_id/check", func(c *gin.Context) {
		c.JSON(http.StatusOK, localLimitResponse{
			Allowed: true,
			Limit:   2,
		})
	})

	router.GET("/api/quotas/resources/organizations/users/:user_id/check", func(c *gin.Context) {
		s.localUI.mu.RLock()
		resp := quotaToResponse(s.localUI.orgQuota)
		s.localUI.mu.RUnlock()
		c.JSON(http.StatusOK, resp)
	})

	router.PUT("/api/quotas/resources/organizations/users/:user_id", func(c *gin.Context) {
		var req localQuotaUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid json"})
			return
		}
		s.localUI.mu.Lock()
		applyQuotaUpdate(&s.localUI.orgQuota, req, s.localUI.user.ID)
		s.localUI.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"message": "Updated quota successfully"})
	})

	router.GET("/api/quotas/resources/organization-users/organizations/:org_id/check", func(c *gin.Context) {
		s.localUI.mu.RLock()
		resp := quotaToResponse(s.localUI.orgUsersQuota)
		s.localUI.mu.RUnlock()
		c.JSON(http.StatusOK, resp)
	})

	router.PUT("/api/quotas/resources/organization-users/organizations/:org_id", func(c *gin.Context) {
		var req localQuotaUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid json"})
			return
		}
		s.localUI.mu.Lock()
		applyQuotaUpdate(&s.localUI.orgUsersQuota, req, s.localUI.user.ID)
		s.localUI.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"message": "Updated org users quota successfully"})
	})
}

func (s *Server) registerRusticUIRoutes(router gin.IRouter, gemGen *protocol.GemstoneGenerator) {
	router.GET("/rustic/guilds/:guild_id/ws", func(c *gin.Context) {
		guildID := c.Param("guild_id")
		userName := strings.TrimSpace(c.Query("user"))
		if guildID == "" || userName == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if s.store != nil {
			if _, err := s.store.GetGuild(guildID); err != nil {
				c.Status(http.StatusNotFound)
				return
			}
		}

		wsID := idgen.NewShortUUID()
		s.localUI.setWSSession(wsID, wsBootstrapSession{
			guildID: guildID,
			userID:  s.localUI.user.ID,
			user:    userName,
			expires: time.Now().Add(30 * time.Minute),
		})
		c.JSON(http.StatusOK, gin.H{"wsId": wsID})
	})

	userHandler := gateway.UserCommsProxyCompatHandler(s.msgClient, s.store, gemGen)
	sysHandler := gateway.SysCommsProxyCompatHandler(s.msgClient, s.store, gemGen)

	router.GET("/rustic/ws/:ws_id/usercomms", func(c *gin.Context) {
		session, ok := s.localUI.getWSSession(c.Param("ws_id"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		c.Request.SetPathValue("id", session.guildID)
		c.Request.SetPathValue("user_id", session.userID)
		c.Request.SetPathValue("user_name", session.user)
		userHandler(c.Writer, c.Request)
	})

	router.GET("/rustic/ws/:ws_id/syscomms", func(c *gin.Context) {
		session, ok := s.localUI.getWSSession(c.Param("ws_id"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		c.Request.SetPathValue("id", session.guildID)
		c.Request.SetPathValue("user_id", session.userID)
		sysHandler(c.Writer, c.Request)
	})
}
