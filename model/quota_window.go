package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 5h/7d fixed quota windows (issue #5). Usage is charged quota per user and
// window; limits are resolved user > group > global, 0 meaning no limit.

const (
	QuotaWindowKind5h = "5h"
	QuotaWindowKind7d = "7d"

	QuotaWindowScopeGlobal = "global"
	QuotaWindowScopeGroup  = "group"
	QuotaWindowScopeUser   = "user"
)

const (
	quotaWindowUsageRedisPrefix = "quota_window_usage"
	quotaWindowUsageRedisTTL5h  = 5*3600 + 3600
	quotaWindowUsageRedisTTL7d  = 7*24*3600 + 3600
	quotaWindowLimitCacheTTL    = 30 * time.Second
)

// QuotaWindowUsage is one user's charged quota in one fixed window. The unique
// key is (user_id, window_kind, window_start), so a request only ever updates
// the row of the window it belongs to.
type QuotaWindowUsage struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	UserId      int    `json:"user_id" gorm:"not null;uniqueIndex:idx_quota_window_usage,priority:1"`
	WindowKind  string `json:"window_kind" gorm:"type:varchar(8);not null;uniqueIndex:idx_quota_window_usage,priority:2"`
	WindowStart int64  `json:"window_start" gorm:"type:bigint;not null;uniqueIndex:idx_quota_window_usage,priority:3"`
	UsedQuota   int64  `json:"used_quota" gorm:"type:bigint;not null;default:0"`
	UpdatedAt   int64  `json:"updated_at" gorm:"type:bigint;not null;default:0"`
}

func (usage *QuotaWindowUsage) BeforeCreate(tx *gorm.DB) error {
	if usage.UpdatedAt == 0 {
		usage.UpdatedAt = common.GetTimestamp()
	}
	return nil
}

// QuotaWindowLimit is an administrator-managed cap for a window. ScopeType is
// "global", "group" or "user"; ScopeKey is empty for global, the group name or
// the numeric user id otherwise. LimitQuota 0 means no limit.
type QuotaWindowLimit struct {
	Id         int    `json:"id" gorm:"primaryKey"`
	ScopeType  string `json:"scope_type" gorm:"type:varchar(8);not null;uniqueIndex:idx_quota_window_limit,priority:1"`
	ScopeKey   string `json:"scope_key" gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_quota_window_limit,priority:2"`
	WindowKind string `json:"window_kind" gorm:"type:varchar(8);not null;uniqueIndex:idx_quota_window_limit,priority:3"`
	LimitQuota int64  `json:"limit_quota" gorm:"type:bigint;not null;default:0"`
	UpdatedAt  int64  `json:"updated_at" gorm:"type:bigint;not null;default:0"`
}

func (limit *QuotaWindowLimit) BeforeCreate(tx *gorm.DB) error {
	limit.UpdatedAt = common.GetTimestamp()
	return nil
}

func (limit *QuotaWindowLimit) BeforeUpdate(tx *gorm.DB) error {
	limit.UpdatedAt = common.GetTimestamp()
	return nil
}

const quotaWindowUsageRedisScript = `
local start = tonumber(redis.call('HGET', KEYS[1], 'start') or '0')
local used = tonumber(redis.call('HGET', KEYS[1], 'used') or '0')
if start ~= tonumber(ARGV[1]) then
  used = 0
end
used = used + tonumber(ARGV[2])
if used < 0 then
  used = 0
end
redis.call('HSET', KEYS[1], 'start', ARGV[1], 'used', used)
redis.call('EXPIRE', KEYS[1], ARGV[3])
return used`

func isQuotaWindowKind(kind string) bool {
	return kind == QuotaWindowKind5h || kind == QuotaWindowKind7d
}

func quotaWindowUsageRedisKey(userId int, kind string) string {
	return fmt.Sprintf("%s:%d:%s", quotaWindowUsageRedisPrefix, userId, kind)
}

func quotaWindowUsageRedisTTL(kind string) int64 {
	if kind == QuotaWindowKind7d {
		return quotaWindowUsageRedisTTL7d
	}
	return quotaWindowUsageRedisTTL5h
}

// RecordQuotaWindowUsage adds delta (can be negative for refunds) to the
// user's window. The Redis counter is the hot path; the database row keeps the
// durable value. A negative total is clamped to 0 so refunds never make a
// window "available" beyond its limit.
func RecordQuotaWindowUsage(userId int, kind string, windowStart int64, delta int64) error {
	if userId <= 0 || delta == 0 || !isQuotaWindowKind(kind) || windowStart <= 0 {
		return nil
	}
	if common.RedisEnabled && common.RDB != nil {
		if _, err := common.RDB.Eval(context.Background(), quotaWindowUsageRedisScript,
			[]string{quotaWindowUsageRedisKey(userId, kind)},
			windowStart, delta, quotaWindowUsageRedisTTL(kind)).Int64(); err != nil {
			common.SysLog(common.LogText("failed to update redis quota window usage (user=%d kind=%s): %s", userId, kind, err.Error()))
		}
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	initial := delta
	if initial < 0 {
		initial = 0
	}
	usage := QuotaWindowUsage{
		UserId:      userId,
		WindowKind:  kind,
		WindowStart: windowStart,
		UsedQuota:   initial,
		UpdatedAt:   common.GetTimestamp(),
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "window_kind"}, {Name: "window_start"}},
		DoUpdates: clause.Assignments(map[string]any{
			"used_quota": gorm.Expr("CASE WHEN quota_window_usages.used_quota + ? < 0 THEN 0 ELSE quota_window_usages.used_quota + ? END", delta, delta),
			"updated_at": usage.UpdatedAt,
		}),
	}).Create(&usage).Error
}

// GetQuotaWindowUsage returns the charged quota in one window, preferring the
// Redis counter when it belongs to the requested window.
func GetQuotaWindowUsage(userId int, kind string, windowStart int64) int64 {
	if userId <= 0 || !isQuotaWindowKind(kind) || windowStart <= 0 {
		return 0
	}
	if common.RedisEnabled && common.RDB != nil {
		values, err := common.RDB.HMGet(context.Background(), quotaWindowUsageRedisKey(userId, kind), "start", "used").Result()
		if err == nil && len(values) == 2 && values[0] != nil && values[1] != nil {
			start, startErr := strconv.ParseInt(fmt.Sprint(values[0]), 10, 64)
			used, usedErr := strconv.ParseInt(fmt.Sprint(values[1]), 10, 64)
			if startErr == nil && usedErr == nil && start == windowStart {
				return used
			}
		}
	}
	if DB == nil {
		return 0
	}
	var usage QuotaWindowUsage
	if err := DB.Where("user_id = ? AND window_kind = ? AND window_start = ?", userId, kind, windowStart).First(&usage).Error; err != nil {
		return 0
	}
	return usage.UsedQuota
}

// SetQuotaWindowLimit upserts one limit row. scopeKey is ignored for global.
func SetQuotaWindowLimit(scopeType string, scopeKey string, kind string, limitQuota int64) error {
	if scopeType != QuotaWindowScopeGlobal && scopeType != QuotaWindowScopeGroup && scopeType != QuotaWindowScopeUser {
		return fmt.Errorf("invalid quota window scope: %s", scopeType)
	}
	if !isQuotaWindowKind(kind) {
		return fmt.Errorf("invalid quota window kind: %s", kind)
	}
	if limitQuota < 0 {
		return errors.New("quota window limit cannot be negative")
	}
	if scopeType == QuotaWindowScopeGlobal {
		scopeKey = ""
	}
	if scopeKey == "" && scopeType != QuotaWindowScopeGlobal {
		return fmt.Errorf("quota window scope %s requires a scope key", scopeType)
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	limit := QuotaWindowLimit{
		ScopeType:  scopeType,
		ScopeKey:   scopeKey,
		WindowKind: kind,
		LimitQuota: limitQuota,
		UpdatedAt:  common.GetTimestamp(),
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope_type"}, {Name: "scope_key"}, {Name: "window_kind"}},
		DoUpdates: clause.Assignments(map[string]any{"limit_quota": limitQuota, "updated_at": limit.UpdatedAt}),
	}).Create(&limit).Error; err != nil {
		return err
	}
	InvalidateQuotaWindowLimitCache()
	return nil
}

// DeleteQuotaWindowLimit removes one limit row; absent rows are a no-op.
func DeleteQuotaWindowLimit(scopeType string, scopeKey string, kind string) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	if scopeType == QuotaWindowScopeGlobal {
		scopeKey = ""
	}
	if err := DB.Where("scope_type = ? AND scope_key = ? AND window_kind = ?", scopeType, scopeKey, kind).
		Delete(&QuotaWindowLimit{}).Error; err != nil {
		return err
	}
	InvalidateQuotaWindowLimitCache()
	return nil
}

type quotaWindowLimitCacheEntry struct {
	limit     int64
	expiresAt time.Time
}

var quotaWindowLimitCache sync.Map

// InvalidateQuotaWindowLimitCache drops the process-local limit cache. It is
// called after every limit write; other replicas converge within the 30s TTL.
func InvalidateQuotaWindowLimitCache() {
	quotaWindowLimitCache.Range(func(key, value any) bool {
		quotaWindowLimitCache.Delete(key)
		return true
	})
}

func resolveQuotaWindowLimitFromDB(userId int, group string, kind string) int64 {
	if DB == nil {
		return 0
	}
	scopes := []struct {
		scopeType string
		scopeKey  string
	}{
		{QuotaWindowScopeUser, strconv.Itoa(userId)},
		{QuotaWindowScopeGroup, group},
		{QuotaWindowScopeGlobal, ""},
	}
	for _, scope := range scopes {
		if scope.scopeType == QuotaWindowScopeGroup && scope.scopeKey == "" {
			continue
		}
		var limits []QuotaWindowLimit
		if err := DB.Where("scope_type = ? AND scope_key = ? AND window_kind = ?", scope.scopeType, scope.scopeKey, kind).
			Limit(1).Find(&limits).Error; err != nil || len(limits) == 0 {
			continue
		}
		limit := limits[0]
		if limit.LimitQuota > 0 {
			return limit.LimitQuota
		}
		return 0
	}
	return 0
}

// GetQuotaWindowLimit resolves the effective limit for a user's window:
// per-user first, then the group, then the global default. 0 means no limit.
func GetQuotaWindowLimit(userId int, group string, kind string) int64 {
	if userId <= 0 || !isQuotaWindowKind(kind) {
		return 0
	}
	cacheKey := fmt.Sprintf("%d|%s|%s", userId, group, kind)
	if cached, ok := quotaWindowLimitCache.Load(cacheKey); ok {
		if entry, ok := cached.(quotaWindowLimitCacheEntry); ok && time.Now().Before(entry.expiresAt) {
			return entry.limit
		}
	}
	limit := resolveQuotaWindowLimitFromDB(userId, group, kind)
	quotaWindowLimitCache.Store(cacheKey, quotaWindowLimitCacheEntry{limit: limit, expiresAt: time.Now().Add(quotaWindowLimitCacheTTL)})
	return limit
}
