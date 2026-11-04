package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const privacyLogTestKey = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"

func setupPrivacyLogTest(t *testing.T, mode, policyJSON string) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousLogType := common.LogDatabaseType()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Log{}, &AuditLog{}))
	DB = database
	LOG_DB = database
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
	})
	t.Setenv("PRIVACY_LOG_MODE", mode)
	t.Setenv("PRIVACY_POLICY_JSON", policyJSON)
	resetLogPrivacyEngineForTest()
	t.Cleanup(resetLogPrivacyEngineForTest)
	return database
}

func TestLogPrivacyMasksLogRow(t *testing.T) {
	db := setupPrivacyLogTest(t, "redact", "")
	require.NoError(t, createLog(&Log{
		UserId: 1,
		Type:   LogTypeError,
		Content: "upstream said key " + privacyLogTestKey,
		Other:   `{"error":"token ` + privacyLogTestKey + `"}`,
	}))
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.NotContains(t, row.Content, privacyLogTestKey)
	assert.Contains(t, row.Content, "{{WINGS_API_KEY_")
	assert.NotContains(t, row.Other, privacyLogTestKey)
	assert.Contains(t, row.Other, "{{WINGS_API_KEY_")
}

func TestLogPrivacyMasksConsumeLog(t *testing.T) {
	db := setupPrivacyLogTest(t, "redact", "")
	other := NewLogOther()
	other.SetPublic("error", "failed with "+privacyLogTestKey)
	require.NoError(t, createLog(&Log{
		UserId:  2,
		Type:    LogTypeConsume,
		Content: "request failed: " + privacyLogTestKey,
		Other:   other.JSONString(),
	}))
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.NotContains(t, row.Content, privacyLogTestKey)
	assert.NotContains(t, row.Other, privacyLogTestKey)
}

func TestLogPrivacyMasksAuditLog(t *testing.T) {
	db := setupPrivacyLogTest(t, "redact", "")
	RecordAuditLog(nil, AuditLog{
		UserId:    1,
		Username:  "root",
		ActorRole: common.RoleRootUser,
		Content:   "rotated key " + privacyLogTestKey,
		Other: AuditOther{
			Op: &AuditOperation{Action: "option.update", Params: AuditFields{"note": "value " + privacyLogTestKey}},
		},
	})
	var row AuditLog
	require.NoError(t, db.Table("audit_logs").Take(&row).Error)
	assert.NotContains(t, row.Content, privacyLogTestKey)
	encoded, err := common.Marshal(row.Other)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), privacyLogTestKey)
}

func TestLogPrivacyOffKeepsContent(t *testing.T) {
	db := setupPrivacyLogTest(t, "off", "")
	require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeError, Content: privacyLogTestKey}))
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, privacyLogTestKey, row.Content)
}

func TestLogPrivacyWarnKeepsContent(t *testing.T) {
	db := setupPrivacyLogTest(t, "warn", "")
	require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeError, Content: privacyLogTestKey}))
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, privacyLogTestKey, row.Content)
}

func TestLogPrivacyPolicyOverride(t *testing.T) {
	policy := `{"enabled":true,"rules":[{"id":"email","type":"email","pattern":"[A-Za-z0-9._%+\\-]+@[A-Za-z0-9.\\-]+\\.[A-Za-z]{2,}","action":"redact","builtin":true}]}`
	db := setupPrivacyLogTest(t, "redact", policy)
	require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeError, Content: "mail li.wei@acme-corp.cn"}))
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.NotContains(t, row.Content, "li.wei@acme-corp.cn")
	assert.Contains(t, row.Content, "{{WINGS_EMAIL_")
}

func TestLogPrivacyErrorLogConsoleContentIsMasked(t *testing.T) {
	db := setupPrivacyLogTest(t, "redact", "")
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	RecordErrorLog(c, 1, 1, "gpt-4o-mini", "token", "boom "+privacyLogTestKey, 1, 1, false, "default", NewLogOther())
	var row Log
	require.NoError(t, db.First(&row).Error)
	assert.NotContains(t, row.Content, privacyLogTestKey)
}

func TestSanitizeLogTextPlaceholderFormat(t *testing.T) {
	setupPrivacyLogTest(t, "redact", "")
	masked := sanitizeLogText("key " + privacyLogTestKey)
	assert.NotContains(t, masked, privacyLogTestKey)
	assert.Contains(t, masked, "{{WINGS_API_KEY_")
	assert.Contains(t, masked, "}}")
}
