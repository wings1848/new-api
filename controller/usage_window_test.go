package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This file covers the window-dimension usage APIs (issue #31 backend):
// per-user window summary with exhaustion estimate, and the admin window report.

func setupUsageWindowTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	return db
}

func insertUsageLog(t *testing.T, db *gorm.DB, userId int, createdAt int64, quota int, group, modelName string) {
	t.Helper()
	require.NoError(t, db.Create(&model.Log{
		UserId:           userId,
		CreatedAt:        createdAt,
		Type:             model.LogTypeConsume,
		Quota:            quota,
		PromptTokens:     10,
		CompletionTokens: 5,
		Group:            group,
		ModelName:        modelName,
		Username:         fmt.Sprintf("user-%d", userId),
	}).Error)
}

func performUsageWindowRequest(t *testing.T, target string, operator model.User, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Set("id", operator.Id)
	c.Set("role", operator.Role)
	c.Set("username", operator.Username)
	handler(c)
	return recorder
}

type usageWindowSummaryJSON struct {
	Success bool `json:"success"`
	Data    struct {
		Windows []struct {
			Kind             string `json:"kind"`
			Limit            int64  `json:"limit"`
			Used             int64  `json:"used"`
			Remaining        int64  `json:"remaining"`
			ResetAt          int64  `json:"reset_at"`
			Source           string `json:"source"`
			EstimatedExhaust int64  `json:"estimated_exhaust_seconds"`
		} `json:"windows"`
	} `json:"data"`
}

func TestUsageWindowSummary(t *testing.T) {
	db := setupUsageWindowTestDB(t)
	user := createQuotaWindowAPIUser(t, db, common.RoleCommonUser, "default")
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 1000))
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind7d, 5000))
	now := time.Now()
	start5h := service.QuotaWindowStart(model.QuotaWindowKind5h, now)
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, start5h, 400))
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, service.QuotaWindowStart(model.QuotaWindowKind7d, now), 900))
	insertUsageLog(t, db, user.Id, now.Unix()-60, 100, "default", "mock-gpt")

	recorder := performUsageWindowRequest(t, "/api/usage-window/summary", user, GetUsageWindowSummary)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response usageWindowSummaryJSON
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	require.Len(t, response.Data.Windows, 2)
	window5h := response.Data.Windows[0]
	assert.Equal(t, "5h", window5h.Kind)
	assert.EqualValues(t, 1000, window5h.Limit)
	assert.EqualValues(t, 400, window5h.Used)
	assert.EqualValues(t, 600, window5h.Remaining)
	assert.Greater(t, window5h.ResetAt, now.Unix())
	assert.Equal(t, "global", window5h.Source)
	assert.Greater(t, window5h.EstimatedExhaust, int64(0), "recent spend should produce an exhaustion estimate")
	assert.EqualValues(t, 900, response.Data.Windows[1].Used)
}

type usageWindowReportJSON struct {
	Success bool `json:"success"`
	Data    struct {
		Window     string `json:"window"`
		GroupBy    string `json:"group_by"`
		Days       int    `json:"days"`
		TotalQuota int64  `json:"total_quota"`
		Rows       []struct {
			Key         string `json:"key"`
			WindowStart int64  `json:"window_start"`
			WindowEnd   int64  `json:"window_end"`
			Quota       int64  `json:"quota"`
			Requests    int64  `json:"requests"`
		} `json:"rows"`
	} `json:"data"`
}

func TestUsageWindowReport(t *testing.T) {
	db := setupUsageWindowTestDB(t)
	now := time.Now()
	start5h := service.QuotaWindowStart(model.QuotaWindowKind5h, now)
	insertUsageLog(t, db, 1, start5h+10, 100, "default", "gpt-a")
	insertUsageLog(t, db, 2, start5h+20, 50, "default", "gpt-a")
	insertUsageLog(t, db, 1, start5h+30, 30, "vip", "gpt-b")

	operator := createQuotaWindowAPIUser(t, db, common.RoleRootUser, "default")
	recorder := performUsageWindowRequest(t, "/api/usage-window/report?window=5h&days=1&group_by=group", operator, GetUsageWindowReport)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response usageWindowReportJSON
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	assert.Equal(t, "5h", response.Data.Window)
	assert.Equal(t, "group", response.Data.GroupBy)
	assert.EqualValues(t, 180, response.Data.TotalQuota)
	require.Len(t, response.Data.Rows, 2)
	byKey := map[string]int64{}
	for _, row := range response.Data.Rows {
		byKey[row.Key] = row.Quota
		assert.Equal(t, start5h, row.WindowStart)
		assert.Equal(t, start5h+18000, row.WindowEnd)
	}
	assert.EqualValues(t, 150, byKey["default"])
	assert.EqualValues(t, 30, byKey["vip"])

	recorder = performUsageWindowRequest(t, "/api/usage-window/report?window=7d&days=7&group_by=model", operator, GetUsageWindowReport)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	byKey = map[string]int64{}
	for _, row := range response.Data.Rows {
		byKey[row.Key] += row.Quota
	}
	assert.EqualValues(t, 150, byKey["gpt-a"])
	assert.EqualValues(t, 30, byKey["gpt-b"])

	// Invalid parameters are rejected.
	recorder = performUsageWindowRequest(t, "/api/usage-window/report?window=1h", operator, GetUsageWindowReport)
	var failed struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &failed), recorder.Body.String())
	assert.False(t, failed.Success)
}

var _ = strings.TrimSpace
