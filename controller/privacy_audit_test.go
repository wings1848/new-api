package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This file covers the privacy dry-run audit APIs (issue #13 backend):
// statistics, paginated events and retention cleanup.

type privacyAuditStatsJSON struct {
	WindowDays    int   `json:"window_days"`
	TotalEvents   int64 `json:"total_events"`
	TotalFindings int64 `json:"total_findings"`
	AffectedUsers int64 `json:"affected_users"`
	ByType        []struct {
		Key   string `json:"key"`
		Count int64  `json:"count"`
	} `json:"by_type"`
	ByAction []struct {
		Key   string `json:"key"`
		Count int64  `json:"count"`
	} `json:"by_action"`
	ByRule []struct {
		Key   string `json:"key"`
		Count int64  `json:"count"`
	} `json:"by_rule"`
	ByMode []struct {
		Key   string `json:"key"`
		Count int64  `json:"count"`
	} `json:"by_mode"`
	Daily []struct {
		Date     string `json:"date"`
		Events   int64  `json:"events"`
		Findings int64  `json:"findings"`
	} `json:"daily"`
}

type privacyAuditEventJSON struct {
	Id           int    `json:"id"`
	UserId       int    `json:"user_id"`
	Username     string `json:"username"`
	RequestId    string `json:"request_id"`
	Path         string `json:"path"`
	Mode         string `json:"mode"`
	FindingCount int    `json:"finding_count"`
	CreatedAt    int64  `json:"created_at"`
	Findings     []struct {
		RuleId string `json:"rule_id"`
		Type   string `json:"type"`
		Action string `json:"action"`
		Count  int    `json:"count"`
	} `json:"findings"`
}

func setupPrivacyAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.PrivacyAuditEvent{}, &model.PrivacyAuditFinding{}))
	return db
}

func seedPrivacyAuditEvent(t *testing.T, userId int, mode string, createdAt int64, findings []model.PrivacyAuditFinding) model.PrivacyAuditEvent {
	t.Helper()
	event := &model.PrivacyAuditEvent{
		UserId:       userId,
		Username:     fmt.Sprintf("user-%d", userId),
		RequestId:    fmt.Sprintf("req-%d-%d", userId, createdAt),
		Path:         "/v1/chat/completions",
		Mode:         mode,
		FindingCount: len(findings),
		CreatedAt:    createdAt,
	}
	require.NoError(t, model.RecordPrivacyAuditEvent(event, findings))
	return *event
}

func performPrivacyAuditRequest(t *testing.T, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	c.Set("username", "root")
	switch {
	case strings.Contains(target, "/stats"):
		GetPrivacyAuditStats(c)
	case strings.Contains(target, "/events"):
		GetPrivacyAuditEvents(c)
	case strings.Contains(target, "/cleanup"):
		CleanupPrivacyAuditEvents(c)
	default:
		t.Fatalf("unexpected privacy audit target: %s", target)
	}
	return recorder
}

func TestPrivacyAuditStats(t *testing.T) {
	setupPrivacyAuditTestDB(t)
	now := time.Now().Unix()
	seedPrivacyAuditEvent(t, 1, "warn", now-3600, []model.PrivacyAuditFinding{
		{RuleId: "api-key", Type: "api_key", Action: "redact", Count: 2},
		{RuleId: "email", Type: "email", Action: "warn", Count: 1},
	})
	seedPrivacyAuditEvent(t, 2, "redact", now-7200, []model.PrivacyAuditFinding{
		{RuleId: "api-key", Type: "api_key", Action: "redact", Count: 3},
	})

	recorder := performPrivacyAuditRequest(t, http.MethodGet, "/api/privacy-audit/stats?days=7", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool                  `json:"success"`
		Data    privacyAuditStatsJSON `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	stats := response.Data
	assert.Equal(t, 7, stats.WindowDays)
	assert.EqualValues(t, 2, stats.TotalEvents)
	assert.EqualValues(t, 6, stats.TotalFindings)
	assert.EqualValues(t, 2, stats.AffectedUsers)
	require.NotEmpty(t, stats.ByType)
	assert.Equal(t, "api_key", stats.ByType[0].Key)
	assert.EqualValues(t, 5, stats.ByType[0].Count)
	assert.NotEmpty(t, stats.Daily)
}

func TestPrivacyAuditEventsPagination(t *testing.T) {
	setupPrivacyAuditTestDB(t)
	now := time.Now().Unix()
	seedPrivacyAuditEvent(t, 1, "warn", now-100, []model.PrivacyAuditFinding{{RuleId: "api-key", Type: "api_key", Action: "redact", Count: 1}})
	seedPrivacyAuditEvent(t, 2, "warn", now-200, []model.PrivacyAuditFinding{{RuleId: "email", Type: "email", Action: "warn", Count: 2}})
	seedPrivacyAuditEvent(t, 1, "block", now-300, []model.PrivacyAuditFinding{{RuleId: "jwt", Type: "jwt", Action: "block", Count: 1}})

	recorder := performPrivacyAuditRequest(t, http.MethodGet, "/api/privacy-audit/events?page=1&page_size=2&days=7", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Items []privacyAuditEventJSON `json:"items"`
			Total int64                   `json:"total"`
			Page  int                     `json:"page"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	assert.EqualValues(t, 3, response.Data.Total)
	require.Len(t, response.Data.Items, 2)
	assert.Equal(t, 1, response.Data.Items[0].UserId)
	assert.NotEmpty(t, response.Data.Items[0].Findings)

	recorder = performPrivacyAuditRequest(t, http.MethodGet, "/api/privacy-audit/events?page=1&page_size=10&days=7&mode=block", "")
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	assert.EqualValues(t, 1, response.Data.Total)
	require.Len(t, response.Data.Items, 1)
	assert.Equal(t, "block", response.Data.Items[0].Mode)
}

func TestPrivacyAuditCleanup(t *testing.T) {
	db := setupPrivacyAuditTestDB(t)
	now := time.Now().Unix()
	old := seedPrivacyAuditEvent(t, 1, "warn", now-40*86400, []model.PrivacyAuditFinding{{RuleId: "api-key", Type: "api_key", Action: "redact", Count: 1}})
	recent := seedPrivacyAuditEvent(t, 1, "warn", now-3600, []model.PrivacyAuditFinding{{RuleId: "email", Type: "email", Action: "warn", Count: 1}})

	var dbgCount int64
	require.NoError(t, db.Model(&model.PrivacyAuditEvent{}).Count(&dbgCount).Error)
	var dbgEvent model.PrivacyAuditEvent
	require.NoError(t, db.Where("id = ?", old.Id).First(&dbgEvent).Error)
	recorder := performPrivacyAuditRequest(t, http.MethodPost, "/api/privacy-audit/cleanup", `{"older_than_days":30}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			DeletedEvents   int64 `json:"deleted_events"`
			DeletedFindings int64 `json:"deleted_findings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	require.True(t, response.Success)
	assert.EqualValues(t, 1, response.Data.DeletedEvents)
	assert.EqualValues(t, 1, response.Data.DeletedFindings)

	var count int64
	require.NoError(t, db.Model(&model.PrivacyAuditEvent{}).Where("id = ?", old.Id).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&model.PrivacyAuditEvent{}).Where("id = ?", recent.Id).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
