package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// 5h/7d fixed quota windows (issue #5), aligned to UTC+8 per the defaults
// recorded in issue #3: the 5h window starts at 00/05/10/15/20 local time and
// the 7d window starts on Monday 00:00 local time.

const (
	quotaWindow5hSeconds int64 = 5 * 3600
	quotaWindow7dSeconds int64 = 7 * 24 * 3600
)

var quotaWindowLocation = time.FixedZone("UTC+8", 8*3600)

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
func recordUserQuotaWindowUsage(userId int, delta int) {
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
		if err := model.RecordQuotaWindowUsage(userId, kind, start, int64(delta)); err != nil {
			common.SysLog(common.LogText("failed to record quota window usage (user=%d kind=%s): %s", userId, kind, err.Error()))
		}
	}
}

func recordQuotaWindowUsageFromRelay(relayInfo *relaycommon.RelayInfo, quota int) {
	if relayInfo == nil || quota == 0 || relayInfo.IsPlayground {
		return
	}
	recordUserQuotaWindowUsage(relayInfo.UserId, quota)
}
