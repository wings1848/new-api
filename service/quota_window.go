package service

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
)

// 5h/7d fixed quota windows (issue #5), aligned to UTC+8 per the defaults
// recorded in issue #3: the 5h window starts at 00/05/10/15/20 local time and
// the 7d window starts on Monday 00:00 local time. Usage crossing 80% of a
// configured limit sends one reminder per window (issue #8).

const (
	quotaWindow5hSeconds int64 = 5 * 3600
	quotaWindow7dSeconds int64 = 7 * 24 * 3600

	quotaWindowDefaultWarningPercent = 80
)

var quotaWindowLocation = time.FixedZone("UTC+8", 8*3600)

// quotaWindowNotify is the notification sink; replaced in tests.
var quotaWindowNotify = NotifyUser

func quotaWindowWarningPercent() int64 {
	percent := common.GetEnvOrDefault("QUOTA_WINDOW_WARNING_PERCENT", quotaWindowDefaultWarningPercent)
	if percent <= 0 || percent >= 100 {
		return quotaWindowDefaultWarningPercent
	}
	return int64(percent)
}

// QuotaWindowDuration returns the fixed length in seconds of one window kind.
func QuotaWindowDuration(kind string) int64 {
	switch kind {
	case model.QuotaWindowKind5h:
		return quotaWindow5hSeconds
	case model.QuotaWindowKind7d:
		return quotaWindow7dSeconds
	default:
		return 0
	}
}

// QuotaWindowStart returns the unix start of the fixed window containing now.
func QuotaWindowStart(kind string, now time.Time) int64 {
	local := now.In(quotaWindowLocation)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, quotaWindowLocation)
	switch kind {
	case model.QuotaWindowKind5h:
		elapsed := int64(local.Sub(midnight) / time.Second)
		return midnight.Unix() + (elapsed/quotaWindow5hSeconds)*quotaWindow5hSeconds
	case model.QuotaWindowKind7d:
		daysSinceMonday := (int(local.Weekday()) + 6) % 7
		return midnight.AddDate(0, 0, -daysSinceMonday).Unix()
	default:
		return 0
	}
}

// QuotaWindowResetAt returns the unix time at which the current window resets.
func QuotaWindowResetAt(kind string, now time.Time) int64 {
	start := QuotaWindowStart(kind, now)
	duration := QuotaWindowDuration(kind)
	if start <= 0 || duration <= 0 {
		return 0
	}
	return start + duration
}

// recordUserQuotaWindowUsage adds delta quota to both windows of the user at
// the current time. Window accounting is an observability/limit side channel:
// failures are logged and never change the request result.
func recordUserQuotaWindowUsage(userId int, group string, delta int, unlimited bool) {
	if userId <= 0 || delta == 0 || model.DB == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			common.SysError(common.LogText("panic while recording quota window usage (user=%d): %v", userId, recovered))
		}
	}()
	now := time.Now()
	for _, kind := range []string{model.QuotaWindowKind5h, model.QuotaWindowKind7d} {
		start := QuotaWindowStart(kind, now)
		if start <= 0 {
			continue
		}
		limit, _ := model.ResolveQuotaWindowLimit(userId, group, kind)
		if err := model.RecordQuotaWindowUsage(userId, kind, start, int64(delta)); err != nil {
			common.SysLog(common.LogText("failed to record quota window usage (user=%d kind=%s): %s", userId, kind, err.Error()))
			continue
		}
		if delta <= 0 || unlimited || limit <= 0 {
			continue
		}
		used := model.GetQuotaWindowUsage(userId, kind, start)
		notifyQuotaWindowWarning(userId, kind, limit, used, int64(delta), start, QuotaWindowResetAt(kind, now), now)
	}
}

// notifyQuotaWindowWarning sends one reminder when a positive charge crosses
// the configured warning threshold of a window. A Redis SETNX key (one per
// user/kind/window) keeps it to one reminder even when a refund makes the
// window cross the threshold again; without Redis the crossing check keeps it
// to one reminder per crossing.
func notifyQuotaWindowWarning(userId int, kind string, limit int64, used int64, delta int64, windowStart int64, resetAt int64, now time.Time) {
	if limit <= 0 || used <= 0 || delta <= 0 {
		return
	}
	usedBefore := used - delta
	if usedBefore < 0 {
		usedBefore = 0
	}
	thresholdQuota := int64(float64(limit) * float64(quotaWindowWarningPercent()) / 100)
	if usedBefore >= thresholdQuota || used < thresholdQuota {
		return
	}
	if !common.QuotaRemindEnabled {
		return
	}
	if !quotaWindowWarningAllowed(userId, kind, windowStart, resetAt, now) {
		return
	}
	cached, err := model.GetUserCache(userId)
	if err != nil || cached == nil {
		return
	}
	setting := cached.GetSetting()
	if setting.QuotaWarningEnabled != nil && !*setting.QuotaWarningEnabled {
		return
	}
	lang := setting.Language
	percent := int(float64(used) * 100 / float64(limit))
	resetText := time.Unix(resetAt, 0).In(quotaWindowLocation).Format("2006-01-02 15:04 MST")
	title := i18n.Translate(lang, i18n.MsgNotifyQuotaWindowTitle)
	content := i18n.Translate(lang, i18n.MsgNotifyQuotaWindowContent, map[string]any{
		"Window":    kind,
		"Percent":   percent,
		"Remaining": logger.FormatQuota(int(limit - used)),
		"ResetAt":   resetText,
	})
	email := cached.Email
	gopool.Go(func() {
		if err := quotaWindowNotify(userId, email, setting, dto.NewNotify(dto.NotifyTypeQuotaExceed, title, content, []any{title, kind, percent, resetText})); err != nil {
			common.SysError(common.LogText("failed to send quota window reminder to user %d: %s", userId, err.Error()))
		}
	})
}

// quotaWindowWarningAllowed claims the one-reminder slot of one window. It
// returns true when no slot was claimed yet or when Redis is unavailable.
func quotaWindowWarningAllowed(userId int, kind string, windowStart int64, resetAt int64, now time.Time) bool {
	if !common.RedisEnabled || common.RDB == nil {
		return true
	}
	ttlSeconds := resetAt - now.Unix()
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}
	key := fmt.Sprintf("quota_window_warn:%d:%s:%d", userId, kind, windowStart)
	allowed, err := common.RDB.SetNX(context.Background(), key, "1", time.Duration(ttlSeconds)*time.Second).Result()
	if err != nil {
		common.SysLog(common.LogText("failed to claim quota window reminder slot (user=%d kind=%s): %s", userId, kind, err.Error()))
		return true
	}
	return allowed
}

func recordQuotaWindowUsageFromRelay(relayInfo *relaycommon.RelayInfo, quota int) {
	if relayInfo == nil || quota == 0 || relayInfo.IsPlayground {
		return
	}
	recordUserQuotaWindowUsage(relayInfo.UserId, relayInfo.UsingGroup, quota, relayInfo.UserUnlimited)
}
