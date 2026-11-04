package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// quotaWindowNow is the clock the pre-check uses; overridable in tests.
var quotaWindowNow = time.Now

// quotaWindowLocation is only used to render the reset time in the client error.
var quotaWindowLocation = time.FixedZone("UTC+8", 8*3600)

type quotaWindowSnapshot struct {
	kind    string
	limit   int64
	used    int64
	resetAt int64
}

func loadQuotaWindowSnapshot(userId int, group string, kind string, now time.Time) quotaWindowSnapshot {
	limit := model.GetQuotaWindowLimit(userId, group, kind)
	snapshot := quotaWindowSnapshot{
		kind:    kind,
		limit:   limit,
		resetAt: service.QuotaWindowResetAt(kind, now),
	}
	if limit > 0 {
		snapshot.used = model.GetQuotaWindowUsage(userId, kind, service.QuotaWindowStart(kind, now))
	}
	return snapshot
}

func quotaWindowObserveOnly() bool {
	return common.GetEnvOrDefaultBool("QUOTA_WINDOW_OBSERVE_ONLY", true)
}

// QuotaWindowCheck rejects a request whose charged quota already reached a
// configured 5h/7d window limit, before any channel is selected, so a user over
// budget can never be retried across channels. Observe-only mode (the default)
// logs the same decision without rejecting, for a rolling rollout.
func QuotaWindowCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := c.GetInt("id")
		if userId <= 0 || common.GetContextKeyBool(c, constant.ContextKeyUserUnlimitedQuota) {
			c.Next()
			return
		}
		group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		now := quotaWindowNow()
		windows := []quotaWindowSnapshot{
			loadQuotaWindowSnapshot(userId, group, model.QuotaWindowKind5h, now),
			loadQuotaWindowSnapshot(userId, group, model.QuotaWindowKind7d, now),
		}
		exceeded := make([]quotaWindowSnapshot, 0, len(windows))
		for _, window := range windows {
			if window.limit > 0 && window.used >= window.limit {
				exceeded = append(exceeded, window)
			}
		}
		if len(exceeded) == 0 {
			c.Next()
			return
		}
		setQuotaWindowHeaders(c, windows)
		// Both windows must have room before a request is let through, so when
		// both are exceeded the client waits for the later reset.
		worst := exceeded[0]
		for _, window := range exceeded[1:] {
			if window.resetAt > worst.resetAt {
				worst = window
			}
		}
		retryAfter := worst.resetAt - now.Unix()
		if retryAfter < 0 {
			retryAfter = 0
		}
		if quotaWindowObserveOnly() {
			logger.LogWarn(c, common.LogText("quota window observe-only: user %d exceeded the %s window (used=%d limit=%d reset_at=%d)",
				userId, worst.kind, worst.used, worst.limit, worst.resetAt))
			c.Next()
			return
		}
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
		// OpenAI and Anthropic SDKs retry a 429 by default; a quota window is not
		// a transient upstream failure, so tell clients not to.
		c.Header("X-Should-Retry", "false")
		resetAt := time.Unix(worst.resetAt, 0).In(quotaWindowLocation).Format("2006-01-02 15:04:05 MST")
		message := i18n.T(c, i18n.MsgQuotaWindowExceeded, map[string]any{
			"Window":  worst.kind,
			"ResetAt": resetAt,
		})
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, message, types.ErrorCodeQuotaWindowExceeded)
	}
}

func setQuotaWindowHeaders(c *gin.Context, windows []quotaWindowSnapshot) {
	for _, window := range windows {
		if window.limit <= 0 {
			continue
		}
		remaining := window.limit - window.used
		if remaining < 0 {
			remaining = 0
		}
		c.Header("X-RateLimit-Limit-"+window.kind, strconv.FormatInt(window.limit, 10))
		c.Header("X-RateLimit-Remaining-"+window.kind, strconv.FormatInt(remaining, 10))
		c.Header("X-RateLimit-Reset-"+window.kind, strconv.FormatInt(window.resetAt, 10))
	}
}
