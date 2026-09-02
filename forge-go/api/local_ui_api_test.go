package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func setTestLocalIdentity(t *testing.T) {
	t.Helper()
	t.Setenv(localUserIDEnvVar, "local-user-123")
	t.Setenv(localUserNameEnvVar, "Local User")
	t.Setenv(localOrganizationIDEnvVar, "local-org-123")
	t.Setenv(localOrganizationNameEnvVar, "Local")
}

func testLocalUIState(t *testing.T) *localUIState {
	t.Helper()
	identity, err := loadLocalIdentityFromEnvironment()
	require.NoError(t, err)
	return newLocalUIState(identity)
}

func TestBuildRouter_LocalIdentityAndQuotaRoutes(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "true")
	t.Setenv("FORGE_ENABLE_UI_API", "false")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	setTestLocalIdentity(t)

	s := &Server{localUI: testLocalUIState(t)}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=local-user-123", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var users []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	require.Equal(t, "local-user-123", users[0]["id"])

	reqQuota := httptest.NewRequest(http.MethodGet, "/api/quotas/resources/guilds/check?orgId=local-org-123&userId=local-user-123", nil)
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
	t.Setenv(localOrganizationIDEnvVar, "local-org_123")
	t.Setenv(localOrganizationNameEnvVar, "Local Team")

	identity, err := loadLocalIdentityFromEnvironment()
	require.NoError(t, err)
	state := newLocalUIState(identity)
	require.Equal(t, "local-user_123", state.user.ID)
	require.Equal(t, "Rohit Example", state.user.FullName)
	require.Equal(t, "local-org_123", state.org.ID)
	require.Equal(t, "Local Team", state.org.Name)
}

func TestLoadLocalIdentityRejectsUnsafeConfiguredIdentity(t *testing.T) {
	t.Setenv(localUserIDEnvVar, `DOMAIN\\John Smith`)
	t.Setenv(localOrganizationIDEnvVar, "local-org-123")

	_, err := loadLocalIdentityFromEnvironment()
	require.ErrorContains(t, err, localUserIDEnvVar)
}

func TestLoadLocalIdentityRejectsMissingOrganization(t *testing.T) {
	t.Setenv(localUserIDEnvVar, "local-user-123")
	t.Setenv(localOrganizationIDEnvVar, "")

	_, err := loadLocalIdentityFromEnvironment()
	require.ErrorContains(t, err, localOrganizationIDEnvVar)
}

func TestLoadLocalIdentityUsesDisplayNameDefaults(t *testing.T) {
	t.Setenv(localUserIDEnvVar, "local-user-123")
	t.Setenv(localOrganizationIDEnvVar, "local-org-123")

	identity, err := loadLocalIdentityFromEnvironment()
	require.NoError(t, err)
	require.Equal(t, localAnonymousUser, identity.UserName)
	require.Equal(t, localOrganizationName, identity.OrganizationName)
}

func TestServerRejectsMissingLocalIdentityConfiguration(t *testing.T) {
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv(localUserIDEnvVar, "")
	t.Setenv(localOrganizationIDEnvVar, "")

	s := NewServer(nil, nil, nil, nil, nil, ":0")
	require.ErrorContains(t, s.ValidateConfiguration(), localUserIDEnvVar)
}

func TestBuildRouter_LocalIdentitySearchReturnsOnlyConfiguredUser(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "true")
	t.Setenv("FORGE_ENABLE_UI_API", "false")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	t.Setenv(localUserIDEnvVar, "rohit")
	t.Setenv(localUserNameEnvVar, "Rohit Example")
	t.Setenv(localOrganizationIDEnvVar, "local-org-123")

	s := &Server{localUI: testLocalUIState(t)}
	router := s.buildRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=unknown,rohit", nil)
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
	setTestLocalIdentity(t)

	s := &Server{localUI: testLocalUIState(t)}
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
	setTestLocalIdentity(t)

	s := &Server{localUI: testLocalUIState(t)}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/users/search?userIds=local-user-123", nil)
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
	setTestLocalIdentity(t)

	s := &Server{localUI: testLocalUIState(t)}
	router := s.buildRouter()

	req := httptest.NewRequest(http.MethodGet, "/rustic/guilds/guild-1/ws?user=Anonymous%20User", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotEmpty(t, body["wsId"])
}
