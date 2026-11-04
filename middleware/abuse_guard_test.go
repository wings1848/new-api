package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the abuse guard (issue #32): in-flight concurrency slots and
// anomaly detection. The guard is entered by the relay entry middleware.

func setupAbuseGuardTest(t *testing.T, env map[string]string) *miniredis.Miniredis {
	t.Helper()
	gin.SetMode(gin.TestMode)
	privacyInboundTestInit.Do(func() {
		require.NoError(t, i18n.Init())
	})
	previousRedis, previousClient := common.RedisEnabled, common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	common.RDB = client
	for key, value := range env {
		t.Setenv(key, value)
	}
	resetAbuseGuardConfigForTest()
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousRedis
		common.RDB = previousClient
		resetAbuseGuardConfigForTest()
	})
	return server
}

func abuseGuardContext(userId, tokenId int, ip string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.RemoteAddr = ip + ":1234"
	c.Set("id", userId)
	if tokenId > 0 {
		c.Set("token_id", tokenId)
	}
	return c, recorder
}

func TestAbuseGuardConcurrencyPerUser(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED":   "true",
		"CONCURRENCY_LIMIT_PER_USER":  "1",
		"CONCURRENCY_LIMIT_PER_TOKEN": "0",
		"CONCURRENCY_LIMIT_FAIL_OPEN": "true",
		"ABUSE_DETECT_ENABLED":        "false",
	})
	c1, r1 := abuseGuardContext(1, 10, "10.0.0.1")
	release, ok := acquireAbuseGuardSlot(c1)
	require.True(t, ok)
	assert.Equal(t, http.StatusOK, r1.Code)

	c2, r2 := abuseGuardContext(1, 11, "10.0.0.1")
	_, ok = acquireAbuseGuardSlot(c2)
	assert.False(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, r2.Code)
	assert.Equal(t, "false", r2.Header().Get("X-Should-Retry"))
	assert.NotEmpty(t, r2.Header().Get("Retry-After"))

	release()
	c3, r3 := abuseGuardContext(1, 12, "10.0.0.1")
	release3, ok := acquireAbuseGuardSlot(c3)
	require.True(t, ok)
	assert.Equal(t, http.StatusOK, r3.Code)
	release3()

	// Another user is unaffected.
	c4, _ := abuseGuardContext(2, 20, "10.0.0.1")
	release4, ok := acquireAbuseGuardSlot(c4)
	require.True(t, ok)
	release4()
}

func TestAbuseGuardConcurrencyPerToken(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED":   "true",
		"CONCURRENCY_LIMIT_PER_USER":  "0",
		"CONCURRENCY_LIMIT_PER_TOKEN": "1",
		"ABUSE_DETECT_ENABLED":        "false",
	})
	first, _ := abuseGuardContext(1, 10, "")
	release, ok := acquireAbuseGuardSlot(first)
	require.True(t, ok)

	second, r2 := abuseGuardContext(1, 10, "")
	_, ok = acquireAbuseGuardSlot(second)
	assert.False(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, r2.Code)

	otherToken, r3 := abuseGuardContext(1, 11, "")
	releaseOther, ok := acquireAbuseGuardSlot(otherToken)
	require.True(t, ok)
	assert.Equal(t, http.StatusOK, r3.Code)
	releaseOther()
	release()
}

func TestAbuseGuardDisabledAndFailOpen(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED":  "false",
		"ABUSE_DETECT_ENABLED":       "false",
		"CONCURRENCY_LIMIT_PER_USER": "1",
	})
	c, recorder := abuseGuardContext(1, 10, "")
	release, ok := acquireAbuseGuardSlot(c)
	require.True(t, ok)
	release()
	assert.Equal(t, http.StatusOK, recorder.Code)

	// Redis off + fail-open: requests pass.
	t.Setenv("CONCURRENCY_LIMIT_ENABLED", "true")
	t.Setenv("CONCURRENCY_LIMIT_FAIL_OPEN", "true")
	resetAbuseGuardConfigForTest()
	common.RedisEnabled = false
	c2, _ := abuseGuardContext(1, 10, "")
	release2, ok := acquireAbuseGuardSlot(c2)
	require.True(t, ok)
	release2()
}

func TestAbuseGuardFailClosed(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED":   "true",
		"CONCURRENCY_LIMIT_PER_USER":  "1",
		"CONCURRENCY_LIMIT_FAIL_OPEN": "false",
	})
	common.RedisEnabled = false
	resetAbuseGuardConfigForTest()
	c, recorder := abuseGuardContext(1, 10, "")
	_, ok := acquireAbuseGuardSlot(c)
	assert.False(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestAbuseGuardFailureRatioAlert(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED":    "false",
		"ABUSE_DETECT_ENABLED":         "true",
		"ABUSE_FAILURE_MIN_REQUESTS":   "4",
		"ABUSE_FAILURE_RATIO":          "0.75",
		"ABUSE_ALERT_COOLDOWN_SECONDS": "300",
	})
	var alerts atomic.Int64
	var lastRule atomic.Value
	previous := abuseAlertSink
	abuseAlertSink = func(alert abuseAlert) {
		alerts.Add(1)
		lastRule.Store(alert.Rule)
	}
	t.Cleanup(func() { abuseAlertSink = previous })

	for i := 0; i < 4; i++ {
		c, _ := abuseGuardContext(1, 10, "10.0.0.9")
		recordAbuseOutcome(c, &abuseGuardConfig{
			detectEnabled:      true,
			failureWindow:      60 * time.Second,
			failureMinRequests: 4,
			failureRatio:       0.75,
			alertCooldown:      300 * time.Second,
			tokenIPMaxTokens:   5,
		}, http.StatusInternalServerError)
	}
	assert.EqualValues(t, 1, alerts.Load())
	assert.Equal(t, "failure_ratio", lastRule.Load())
}

func TestAbuseGuardTokenRotationAlert(t *testing.T) {
	setupAbuseGuardTest(t, map[string]string{
		"CONCURRENCY_LIMIT_ENABLED": "false",
		"ABUSE_DETECT_ENABLED":      "true",
	})
	var alerts atomic.Int64
	var lastRule atomic.Value
	previous := abuseAlertSink
	abuseAlertSink = func(alert abuseAlert) {
		alerts.Add(1)
		lastRule.Store(alert.Rule)
	}
	t.Cleanup(func() { abuseAlertSink = previous })

	config := &abuseGuardConfig{
		detectEnabled:      true,
		failureWindow:      60 * time.Second,
		failureMinRequests: 100,
		failureRatio:       1,
		alertCooldown:      300 * time.Second,
		tokenIPMaxTokens:   3,
	}
	for tokenId := 1; tokenId <= 3; tokenId++ {
		c, _ := abuseGuardContext(1, tokenId, "10.0.0.8")
		recordAbuseOutcome(c, config, http.StatusOK)
	}
	assert.EqualValues(t, 1, alerts.Load())
	assert.Equal(t, "token_rotation", lastRule.Load())
}
