package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This file covers the 5h/7d pre-check middleware (issue #6): reject an
// already-exceeded window before any channel is selected, with the standard
// rate-limit headers, while the observe-only mode logs instead of rejecting.

var quotaWindowTestInit sync.Once

func setupQuotaWindowTestDB(t *testing.T) {
	t.Helper()
	quotaWindowTestInit.Do(func() {
		require.NoError(t, i18n.Init())
	})
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	model.DB = database
	common.RedisEnabled = false
	model.InvalidateQuotaWindowLimitCache()
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		model.InvalidateQuotaWindowLimitCache()
	})
}

var quotaWindowFixedNow = time.Date(2026, 11, 5, 9, 12, 34, 0, time.FixedZone("UTC+8", 8*3600))

// serveQuotaWindowCheck runs the middleware in a small router that first seeds
// the authenticated user context, then the middleware, then a final handler.
func serveQuotaWindowCheck(t *testing.T, userId int, unlimited bool) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", userId)
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserUnlimitedQuota, unlimited)
		c.Next()
	})
	router.Use(QuotaWindowCheck())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	return recorder
}

func seedQuotaWindowUsage(t *testing.T, userId int, kind string, used int64) int64 {
	t.Helper()
	start := service.QuotaWindowStart(kind, quotaWindowFixedNow)
	require.NoError(t, model.RecordQuotaWindowUsage(userId, kind, start, used))
	return start
}

func withQuotaWindowClock(t *testing.T) {
	t.Helper()
	previous := quotaWindowNow
	quotaWindowNow = func() time.Time { return quotaWindowFixedNow }
	t.Cleanup(func() { quotaWindowNow = previous })
}

func TestQuotaWindowCheckNoLimitPasses(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "false")

	recorder := serveQuotaWindowCheck(t, 1, false)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestQuotaWindowCheckRejectsExceeded5h(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "false")
	userId := 2
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	start := seedQuotaWindowUsage(t, userId, model.QuotaWindowKind5h, 100)

	recorder := serveQuotaWindowCheck(t, userId, false)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "quota_window_exceeded")
	assert.Equal(t, "100", recorder.Header().Get("X-RateLimit-Limit-5h"))
	assert.Equal(t, "0", recorder.Header().Get("X-RateLimit-Remaining-5h"))
	assert.Equal(t, itoa64(start+service.QuotaWindowDuration(model.QuotaWindowKind5h)), recorder.Header().Get("X-RateLimit-Reset-5h"))
	assert.Equal(t, "false", recorder.Header().Get("X-Should-Retry"))
	assert.NotEmpty(t, recorder.Header().Get("Retry-After"))
}

func TestQuotaWindowCheckBelowLimitPasses(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "false")
	userId := 3
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	seedQuotaWindowUsage(t, userId, model.QuotaWindowKind5h, 40)

	recorder := serveQuotaWindowCheck(t, userId, false)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestQuotaWindowCheckObserveOnlyPasses(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "true")
	userId := 4
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	seedQuotaWindowUsage(t, userId, model.QuotaWindowKind5h, 100)

	recorder := serveQuotaWindowCheck(t, userId, false)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestQuotaWindowCheckUnlimitedUserPasses(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "false")
	userId := 5
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	seedQuotaWindowUsage(t, userId, model.QuotaWindowKind5h, 100)

	recorder := serveQuotaWindowCheck(t, userId, true)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestQuotaWindowCheckBothWindowsRetryAfter(t *testing.T) {
	setupQuotaWindowTestDB(t)
	withQuotaWindowClock(t)
	t.Setenv("QUOTA_WINDOW_OBSERVE_ONLY", "false")
	userId := 6
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 10))
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind7d, 20))
	seedQuotaWindowUsage(t, userId, model.QuotaWindowKind5h, 10)
	seedQuotaWindowUsage(t, userId, model.QuotaWindowKind7d, 20)

	recorder := serveQuotaWindowCheck(t, userId, false)
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	latestReset := service.QuotaWindowResetAt(model.QuotaWindowKind7d, quotaWindowFixedNow)
	wantRetryAfter := latestReset - quotaWindowFixedNow.Unix()
	assert.Equal(t, itoa64(wantRetryAfter), recorder.Header().Get("Retry-After"))
}

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
