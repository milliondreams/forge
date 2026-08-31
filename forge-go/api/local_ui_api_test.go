package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildRouter_LocalIdentityAndQuotaRoutes(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "true")
	t.Setenv("FORGE_ENABLE_UI_API", "false")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	s := &Server{localUI: newLocalUIState()}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=dummyuserid", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var users []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	require.Equal(t, "dummyuserid", users[0]["id"])

	reqQuota := httptest.NewRequest(http.MethodGet, "/api/quotas/resources/guilds/check?orgId=acmeorganizationid&userId=dummyuserid", nil)
	rrQuota := httptest.NewRecorder()
	router.ServeHTTP(rrQuota, reqQuota)
	require.Equal(t, http.StatusOK, rrQuota.Code)

	var quota map[string]interface{}
	require.NoError(t, json.Unmarshal(rrQuota.Body.Bytes(), &quota))
	require.Equal(t, true, quota["allowed"])
	require.Equal(t, float64(25), quota["limit"])
}

func TestNewLocalUIStateUsesValidatedConfiguredIdentity(t *testing.T) {
	t.Setenv(localUserIDEnvVar, "local-user_123")
	t.Setenv(localUserNameEnvVar, "Rohit Example")

	state := newLocalUIState()
	require.Equal(t, "local-user_123", state.user.ID)
	require.Equal(t, "Rohit Example", state.user.FullName)
}

func TestNewLocalUIStateRejectsUnsafeConfiguredIdentity(t *testing.T) {
	t.Setenv(localUserIDEnvVar, `DOMAIN\\John Smith`)

	state := newLocalUIState()
	require.Equal(t, localDummyUserID, state.user.ID)
}

func TestBuildRouter_LocalIdentitySearchReturnsOnlyConfiguredUser(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "true")
	t.Setenv("FORGE_ENABLE_UI_API", "false")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	t.Setenv(localUserIDEnvVar, "rohit")
	t.Setenv(localUserNameEnvVar, "Rohit Example")

	s := &Server{localUI: newLocalUIState()}
	router := s.buildRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=dummyuserid,rohit", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var users []localUserInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Equal(t, []localUserInfo{
		{ID: "rohit", FullName: "Rohit Example", Email: "anonymous@example.com"},
	}, users)
}

func TestBuildRouter_LocalIdentityUsersSearchEmptyIDsReturnsEmptyList(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "true")
	t.Setenv("FORGE_ENABLE_UI_API", "false")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	s := &Server{localUI: newLocalUIState()}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var users []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 0)
}

func TestBuildRouter_DisablePublicAPIRoutes(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	s := &Server{localUI: newLocalUIState()}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=dummyuserid", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)

	reqHealth := httptest.NewRequest(http.MethodGet, "/rustic/__health", nil)
	rrHealth := httptest.NewRecorder()
	router.ServeHTTP(rrHealth, reqHealth)
	require.Equal(t, http.StatusOK, rrHealth.Code)
}

func TestRusticWSBootstrapRoute(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	s := &Server{localUI: newLocalUIState()}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/rustic/guilds/guild-1/ws?user=Anonymous%20User", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotEmpty(t, body["wsId"])
}
