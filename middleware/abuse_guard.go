package middleware

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

// Abuse guard (issue #32), entered from the relay entry middleware:
//   - in-flight concurrency slots per user and per token (Redis), so one client
//     cannot occupy the whole gateway with long streaming requests;
//   - anomaly detection (failure ratio, same user+IP rotating many tokens) with
//     a cooldown and an audit alert.
// Everything is opt-in through environment variables and fails open by default.

const (
	abuseConcurrencyKeyPrefix = "concurrency:"
	abuseUserBucketPrefix     = "abuse:user:"
	abuseIPPrefix             = "abuse:ip:"
	abuseAlertPrefix          = "abuse:alert:"
)

// abuseAlert is one anomaly alert handed to the sink.
type abuseAlert struct {
	UserId int
	Rule   string
	Detail map[string]any
}

// abuseAlertSink receives anomaly alerts; replaced in tests.
var abuseAlertSink = func(alert abuseAlert) {
	common.SysLog(common.LogText("abuse anomaly detected: user=%d rule=%s detail=%v", alert.UserId, alert.Rule, alert.Detail))
	params := map[string]any{"rule": alert.Rule, "user_id": alert.UserId}
	for key, value := range alert.Detail {
		params[key] = value
	}
	model.RecordOperationAuditLog(alert.UserId, common.RoleCommonUser,
		"Detected abuse anomaly: "+alert.Rule, "", "abuse.anomaly", params, nil, nil)
}

type abuseGuardConfig struct {
	concurrencyEnabled bool
	perUser            int
	perToken           int
	slotTTL            time.Duration
	failOpen           bool

	detectEnabled      bool
	failureWindow      time.Duration
	failureMinRequests int
	failureRatio       float64
	tokenIPMaxTokens   int
	alertCooldown      time.Duration
}

var (
	abuseGuardOnce  sync.Once
	abuseGuardValue *abuseGuardConfig
)

// resetAbuseGuardConfigForTest makes the lazily loaded configuration reload on
// the next call (tests change the environment between cases).
func resetAbuseGuardConfigForTest() {
	abuseGuardOnce = sync.Once{}
	abuseGuardValue = nil
}

func abuseGuardFloatEnv(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func loadAbuseGuardConfig() *abuseGuardConfig {
	config := &abuseGuardConfig{
		concurrencyEnabled: common.GetEnvOrDefaultBool("CONCURRENCY_LIMIT_ENABLED", false),
		perUser:            common.GetEnvOrDefault("CONCURRENCY_LIMIT_PER_USER", 0),
		perToken:           common.GetEnvOrDefault("CONCURRENCY_LIMIT_PER_TOKEN", 0),
		slotTTL:            time.Duration(common.GetEnvOrDefault("CONCURRENCY_LIMIT_TTL_SECONDS", 600)) * time.Second,
		failOpen:           common.GetEnvOrDefaultBool("CONCURRENCY_LIMIT_FAIL_OPEN", true),

		detectEnabled:      common.GetEnvOrDefaultBool("ABUSE_DETECT_ENABLED", false),
		failureWindow:      time.Duration(common.GetEnvOrDefault("ABUSE_FAILURE_WINDOW_SECONDS", 60)) * time.Second,
		failureMinRequests: common.GetEnvOrDefault("ABUSE_FAILURE_MIN_REQUESTS", 20),
		failureRatio:       abuseGuardFloatEnv("ABUSE_FAILURE_RATIO", 0.8),
		tokenIPMaxTokens:   common.GetEnvOrDefault("ABUSE_TOKEN_IP_MAX_TOKENS", 5),
		alertCooldown:      time.Duration(common.GetEnvOrDefault("ABUSE_ALERT_COOLDOWN_SECONDS", 300)) * time.Second,
	}
	if config.slotTTL <= 0 {
		config.slotTTL = 10 * time.Minute
	}
	if config.failureWindow <= 0 {
		config.failureWindow = time.Minute
	}
	if config.alertCooldown <= 0 {
		config.alertCooldown = 5 * time.Minute
	}
	if !config.concurrencyEnabled && !config.detectEnabled {
		return nil
	}
	return config
}

func getAbuseGuardConfig() *abuseGuardConfig {
	abuseGuardOnce.Do(func() {
		abuseGuardValue = loadAbuseGuardConfig()
	})
	return abuseGuardValue
}

var acquireConcurrencySlotsScript = redis.NewScript(`
local ttl = tonumber(ARGV[1])
for i = 1, #KEYS do
  local limit = tonumber(ARGV[i + 1])
  local current = redis.call('INCR', KEYS[i])
  if current == 1 then
    redis.call('EXPIRE', KEYS[i], ttl)
  end
  if current > limit then
    for j = 1, i do
      redis.call('DECR', KEYS[j])
    end
    return 0
  end
end
return 1
`)

var releaseConcurrencySlotsScript = redis.NewScript(`
for i = 1, #KEYS do
  local current = tonumber(redis.call('GET', KEYS[i]) or '0')
  if current > 0 then
    redis.call('DECR', KEYS[i])
  end
end
return 1
`)

func abuseConcurrencySlots(c *gin.Context, config *abuseGuardConfig) ([]string, []int) {
	keys := make([]string, 0, 2)
	limits := make([]int, 0, 2)
	if config.perUser > 0 {
		if userId := c.GetInt("id"); userId > 0 {
			keys = append(keys, fmt.Sprintf("%suser:%d", abuseConcurrencyKeyPrefix, userId))
			limits = append(limits, config.perUser)
		}
	}
	if config.perToken > 0 {
		if tokenId := c.GetInt("token_id"); tokenId > 0 {
			keys = append(keys, fmt.Sprintf("%stoken:%d", abuseConcurrencyKeyPrefix, tokenId))
			limits = append(limits, config.perToken)
		}
	}
	return keys, limits
}

// acquireAbuseGuardSlot claims the in-flight slots for this request. It returns
// false (and writes the error response) when the request must be rejected.
func acquireAbuseGuardSlot(c *gin.Context) (func(), bool) {
	config := getAbuseGuardConfig()
	if config == nil || !config.concurrencyEnabled || (config.perUser <= 0 && config.perToken <= 0) {
		return func() {}, true
	}
	keys, limits := abuseConcurrencySlots(c, config)
	if len(keys) == 0 {
		return func() {}, true
	}
	release := func() {
		if common.RedisEnabled && common.RDB != nil {
			_ = releaseConcurrencySlotsScript.Run(context.Background(), common.RDB, keys).Err()
		}
	}
	if !common.RedisEnabled || common.RDB == nil {
		if config.failOpen {
			return func() {}, true
		}
		abortAbuseGuard(c, http.StatusServiceUnavailable, types.ErrorCodeConcurrencyGuardUnavailable, i18n.MsgAbuseGuardUnavailable)
		return func() {}, false
	}
	arguments := make([]any, 0, len(keys)+1)
	arguments = append(arguments, int(config.slotTTL/time.Second))
	for _, limit := range limits {
		arguments = append(arguments, limit)
	}
	result, err := acquireConcurrencySlotsScript.Run(context.Background(), common.RDB, keys, arguments...).Int()
	if err != nil {
		if config.failOpen {
			common.SysLog(common.LogText("concurrency limiter unavailable, failing open: %v", err))
			return func() {}, true
		}
		abortAbuseGuard(c, http.StatusServiceUnavailable, types.ErrorCodeConcurrencyGuardUnavailable, i18n.MsgAbuseGuardUnavailable)
		return func() {}, false
	}
	if result == 0 {
		abortAbuseGuard(c, http.StatusTooManyRequests, types.ErrorCodeConcurrencyLimitExceeded, i18n.MsgConcurrencyLimitExceeded)
		return func() {}, false
	}
	return release, true
}

func abortAbuseGuard(c *gin.Context, status int, code types.ErrorCode, messageKey string) {
	if status == http.StatusTooManyRequests {
		c.Header("Retry-After", "1")
		c.Header("X-Should-Retry", "false")
	}
	message := i18n.T(c, messageKey)
	abortWithOpenAiMessage(c, status, message, code)
}

// recordAbuseOutcome books one finished request and raises at most one alert per
// rule per cooldown when a threshold trips.
func recordAbuseOutcome(c *gin.Context, config *abuseGuardConfig, status int) {
	if config == nil || !config.detectEnabled {
		return
	}
	if !common.RedisEnabled || common.RDB == nil || c == nil {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		return
	}
	ctx := context.Background()
	now := time.Now().Unix()
	windowSeconds := int64(config.failureWindow / time.Second)
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	bucket := now - now%windowSeconds
	userKey := fmt.Sprintf("%s%d:%d", abuseUserBucketPrefix, userId, bucket)
	ip := c.ClientIP()
	tokenId := c.GetInt("token_id")

	pipe := common.RDB.Pipeline()
	totalCommand := pipe.HIncrBy(ctx, userKey, "total", 1)
	var failedCommand *redis.IntCmd
	if status >= 500 {
		failedCommand = pipe.HIncrBy(ctx, userKey, "failed", 1)
	}
	pipe.Expire(ctx, userKey, 2*config.failureWindow)
	if tokenId > 0 && ip != "" {
		ipKey := fmt.Sprintf("%s%d:%s", abuseIPPrefix, userId, ip)
		pipe.SAdd(ctx, ipKey, tokenId)
		pipe.Expire(ctx, ipKey, 2*config.failureWindow)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}

	if config.failureMinRequests > 0 && config.failureRatio > 0 {
		total := totalCommand.Val()
		failed := int64(0)
		if failedCommand != nil {
			failed = failedCommand.Val()
		}
		if total >= int64(config.failureMinRequests) && float64(failed)/float64(total) >= config.failureRatio {
			raiseAbuseAlert(userId, "failure_ratio", map[string]any{
				"total":  total,
				"failed": failed,
				"window": windowSeconds,
			}, config)
		}
	}

	if tokenId > 0 && ip != "" && config.tokenIPMaxTokens > 0 {
		ipKey := fmt.Sprintf("%s%d:%s", abuseIPPrefix, userId, ip)
		if distinct, err := common.RDB.SCard(ctx, ipKey).Result(); err == nil && distinct >= int64(config.tokenIPMaxTokens) {
			raiseAbuseAlert(userId, "token_rotation", map[string]any{
				"ip":     ip,
				"tokens": distinct,
				"window": windowSeconds,
			}, config)
		}
	}
}

func raiseAbuseAlert(userId int, rule string, detail map[string]any, config *abuseGuardConfig) {
	if common.RDB == nil {
		return
	}
	key := fmt.Sprintf("%s%d:%s", abuseAlertPrefix, userId, rule)
	claimed, err := common.RDB.SetNX(context.Background(), key, "1", config.alertCooldown).Result()
	if err != nil || !claimed {
		return
	}
	abuseAlertSink(abuseAlert{UserId: userId, Rule: rule, Detail: detail})
}
