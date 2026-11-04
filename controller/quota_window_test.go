package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This file covers the quota-window admin/self APIs (issue #7): administrators
// configure per-user and per-group 5h/7d limits, users read their own windows.

var quotaWindowAPISeq atomic.Int64

type quotaWindowWindowJSON struct {
	Kind      string `json:"kind"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	ResetAt   int64  `json:"reset_at"`
	Source    string `json:"source"`
}

type quotaWindowOverridesJSON struct {
	FiveHour int64 `json:"window_5h_quota"`
	SevenDay int64 `json:"window_7d_quota"`
}

type quotaWindowViewJSON struct {
	UserID        int                      `json:"user_id"`
	Username      string                   `json:"username"`
	Group         string                   `json:"group"`
	Windows       []quotaWindowWindowJSON  `json:"windows"`
	UserOverrides quotaWindowOverridesJSON `json:"user_overrides"`
}

type quotaWindowAPIResponse struct {
	Success bool                `json:"success"`
	Message string              `json:"message"`
	Data    quotaWindowViewJSON `json:"data"`
}

func setupQuotaWindowAPIDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	model.InvalidateQuotaWindowLimitCache()
	return db
}

func createQuotaWindowAPIUser(t *testing.T, db *gorm.DB, role int, group string) model.User {
	t.Helper()
	seq := quotaWindowAPISeq.Add(1)
	if group == "" {
		group = "default"
	}
	user := model.User{
		Username:    fmt.Sprintf("qw-api-user-%d", seq),
		Password:    "password",
		Role:        role,
		Status:      common.UserStatusEnabled,
		Group:       group,
		AffCode:     fmt.Sprintf("qw-aff-%d", seq),
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func performQuotaWindowAPIRequest(t *testing.T, method, path string, params gin.Params, body string, operator model.User) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", operator.Id)
	c.Set("role", operator.Role)
	common.SetContextKey(c, constant.ContextKeyUserGroup, operator.Group)
	c.Set("username", operator.Username)
	c.Set("session_id", "quota-window-test-session")
	c.Set("auth_version", 1)
	c.Set("session_version", 1)
	callQuotaWindowHandler(t, method, path, c)
	return recorder
}

func decodeQuotaWindowResponse(t *testing.T, recorder *httptest.ResponseRecorder) quotaWindowAPIResponse {
	t.Helper()
	var response quotaWindowAPIResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

func TestQuotaWindowAdminSetUserLimits(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")
	target := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "default")

	recorder := performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":1000,"window_7d_quota":5000}`, operator)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	var limits []model.QuotaWindowLimit
	require.NoError(t, db.Where("scope_type = ? AND scope_key = ?", model.QuotaWindowScopeUser, fmt.Sprintf("%d", target.Id)).
		Order("window_kind").Find(&limits).Error)
	require.Len(t, limits, 2)
	assert.EqualValues(t, 1000, limits[0].LimitQuota)
	assert.EqualValues(t, 5000, limits[1].LimitQuota)

	// Read-back through the admin endpoint.
	recorder = performQuotaWindowAPIRequest(t, http.MethodGet,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}}, "", operator)
	response := decodeQuotaWindowResponse(t, recorder)
	require.True(t, response.Success)
	require.Len(t, response.Data.Windows, 2)
	assert.EqualValues(t, 1000, response.Data.Windows[0].Limit)
	assert.Equal(t, "user", response.Data.Windows[0].Source)
	assert.EqualValues(t, 5000, response.Data.Windows[1].Limit)
}

func TestQuotaWindowAdminUserFollowsGroup(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")
	target := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "team-window")
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGroup, "team-window", model.QuotaWindowKind5h, 400))

	// First pin a user override, then clear it with -1.
	recorder := performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":900}`, operator)
	require.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	recorder = performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":-1}`, operator)
	require.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	var count int64
	require.NoError(t, db.Model(&model.QuotaWindowLimit{}).
		Where("scope_type = ? AND scope_key = ? AND window_kind = ?", model.QuotaWindowScopeUser, fmt.Sprintf("%d", target.Id), model.QuotaWindowKind5h).
		Count(&count).Error)
	assert.Zero(t, count)

	recorder = performQuotaWindowAPIRequest(t, http.MethodGet,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}}, "", operator)
	response := decodeQuotaWindowResponse(t, recorder)
	assert.EqualValues(t, 400, response.Data.Windows[0].Limit)
	assert.Equal(t, "group", response.Data.Windows[0].Source)
}

func TestQuotaWindowAdminUserZeroUnlimited(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")
	target := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "team-zero")
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGroup, "team-zero", model.QuotaWindowKind5h, 400))

	recorder := performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":0}`, operator)
	require.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	recorder = performQuotaWindowAPIRequest(t, http.MethodGet,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}}, "", operator)
	response := decodeQuotaWindowResponse(t, recorder)
	assert.Zero(t, response.Data.Windows[0].Limit)
	assert.Equal(t, "user", response.Data.Windows[0].Source)
}

func TestQuotaWindowSelfReturnsWindows(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	user := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "default")
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind7d, 200))
	now := time.Now()
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, mustQuotaWindowStart(t, model.QuotaWindowKind5h, now), 30))
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, mustQuotaWindowStart(t, model.QuotaWindowKind7d, now), 40))

	recorder := performQuotaWindowAPIRequest(t, http.MethodGet, "/api/quota-window/self", nil, "", user)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	response := decodeQuotaWindowResponse(t, recorder)
	require.True(t, response.Success)
	require.Len(t, response.Data.Windows, 2)
	assert.EqualValues(t, 30, response.Data.Windows[0].Used)
	assert.EqualValues(t, 100, response.Data.Windows[0].Limit)
	assert.EqualValues(t, 70, response.Data.Windows[0].Remaining)
	assert.Greater(t, response.Data.Windows[0].ResetAt, now.Unix())
	assert.Equal(t, "global", response.Data.Windows[0].Source)
	assert.EqualValues(t, 40, response.Data.Windows[1].Used)
	assert.EqualValues(t, 160, response.Data.Windows[1].Remaining)
}

func TestQuotaWindowAdminGroupLimits(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")

	recorder := performQuotaWindowAPIRequest(t, http.MethodPut, "/api/quota-window/admin/groups/team-g",
		gin.Params{{Key: "name", Value: "team-g"}},
		`{"window_5h_quota":123,"window_7d_quota":456}`, operator)
	require.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	recorder = performQuotaWindowAPIRequest(t, http.MethodGet, "/api/quota-window/admin/groups/team-g",
		gin.Params{{Key: "name", Value: "team-g"}}, "", operator)
	response := decodeQuotaWindowResponse(t, recorder)
	require.True(t, response.Success)
	assert.EqualValues(t, 123, response.Data.Windows[0].Limit)
	assert.EqualValues(t, 456, response.Data.Windows[1].Limit)

	var count int64
	require.NoError(t, db.Model(&model.QuotaWindowLimit{}).
		Where("scope_type = ? AND scope_key = ?", model.QuotaWindowScopeGroup, "team-g").Count(&count).Error)
	assert.EqualValues(t, 2, count)
}

func TestQuotaWindowAdminRejectsHigherRole(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleAdminUser, "default")
	target := createQuotaWindowAPIUser(t, db, common.RoleAdminUser, "default")

	recorder := performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":100}`, operator)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.False(t, decodeQuotaWindowResponse(t, recorder).Success)
}

func TestQuotaWindowAdminAuditsUpdate(t *testing.T) {
	db := setupQuotaWindowAPIDB(t)
	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")
	target := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "default")

	recorder := performQuotaWindowAPIRequest(t, http.MethodPut,
		fmt.Sprintf("/api/quota-window/admin/users/%d", target.Id),
		gin.Params{{Key: "id", Value: fmt.Sprintf("%d", target.Id)}},
		`{"window_5h_quota":321}`, operator)
	require.True(t, decodeQuotaWindowResponse(t, recorder).Success)

	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).
		Where("action = ?", "user.quota_window_update").Count(&count).Error)
	assert.Greater(t, count, int64(0))
}

func mustQuotaWindowStart(t *testing.T, kind string, now time.Time) int64 {
	t.Helper()
	return service.QuotaWindowStart(kind, now)
}

func callQuotaWindowHandler(t *testing.T, method, path string, c *gin.Context) {
	t.Helper()
	switch {
	case strings.HasSuffix(path, "/quota-window/self"):
		GetQuotaWindowSelf(c)
	case strings.Contains(path, "/quota-window/admin/users/"):
		if method == http.MethodPut {
			UpdateQuotaWindowAdminUser(c)
			return
		}
		GetQuotaWindowAdminUser(c)
	case strings.Contains(path, "/quota-window/admin/groups/"):
		if method == http.MethodPut {
			UpdateQuotaWindowAdminGroup(c)
			return
		}
		GetQuotaWindowAdminGroup(c)
	default:
		t.Fatalf("unexpected quota window path: %s", path)
	}
}
