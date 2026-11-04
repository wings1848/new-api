package service

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openQuotaWindowTestDB(dialect string, dsn string) (*gorm.DB, error) {
	switch dialect {
	case "mysql":
		return gorm.Open(mysql.Open(dsn), &gorm.Config{})
	case "postgres":
		return gorm.Open(postgres.Open(dsn), &gorm.Config{})
	default:
		return gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	}
}

// This file covers the 5h/7d fixed quota windows (issue #5): window math,
// usage accounting, limit resolution and the billing integration points.

var quotaWindowTZ = time.FixedZone("UTC+8", 8*3600)

func quotaWindowTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, quotaWindowTZ)
	require.NoError(t, err)
	return parsed
}

func resetQuotaWindowTables(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.QuotaWindowUsage{}).Error)
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.QuotaWindowLimit{}).Error)
	model.InvalidateQuotaWindowLimitCache()
}

func TestQuotaWindowStart5hAlignsToUTC8Grid(t *testing.T) {
	cases := []struct {
		at   string
		want string
	}{
		{"2026-11-05 00:00:00", "2026-11-05 00:00:00"},
		{"2026-11-05 04:59:59", "2026-11-05 00:00:00"},
		{"2026-11-05 05:00:00", "2026-11-05 05:00:00"},
		{"2026-11-05 09:12:34", "2026-11-05 05:00:00"},
		{"2026-11-05 20:00:00", "2026-11-05 20:00:00"},
		{"2026-11-05 23:59:59", "2026-11-05 20:00:00"},
	}
	for _, testCase := range cases {
		at := quotaWindowTestTime(t, testCase.at)
		got := time.Unix(QuotaWindowStart(model.QuotaWindowKind5h, at), 0).In(quotaWindowTZ)
		assert.Equal(t, testCase.want, got.Format("2006-01-02 15:04:05"), "at %s", testCase.at)
	}
}

func TestQuotaWindowStart7dAlignsToMondayUTC8(t *testing.T) {
	cases := []struct {
		at   string
		want string
	}{
		// 2026-11-02 is a Monday.
		{"2026-11-02 00:00:00", "2026-11-02 00:00:00"},
		{"2026-11-04 12:00:00", "2026-11-02 00:00:00"},
		{"2026-11-08 23:59:59", "2026-11-02 00:00:00"},
		{"2026-11-09 00:00:00", "2026-11-09 00:00:00"},
		{"2027-01-01 00:00:00", "2026-12-28 00:00:00"},
	}
	for _, testCase := range cases {
		at := quotaWindowTestTime(t, testCase.at)
		got := time.Unix(QuotaWindowStart(model.QuotaWindowKind7d, at), 0).In(quotaWindowTZ)
		assert.Equal(t, testCase.want, got.Format("2006-01-02 15:04:05"), "at %s", testCase.at)
	}
}

func TestQuotaWindowResetAt(t *testing.T) {
	at := quotaWindowTestTime(t, "2026-11-05 09:12:34")
	assert.Equal(t, "2026-11-05 10:00:00",
		time.Unix(QuotaWindowResetAt(model.QuotaWindowKind5h, at), 0).In(quotaWindowTZ).Format("2006-01-02 15:04:05"))
	assert.Equal(t, "2026-11-09 00:00:00",
		time.Unix(QuotaWindowResetAt(model.QuotaWindowKind7d, at), 0).In(quotaWindowTZ).Format("2006-01-02 15:04:05"))
}

func TestQuotaWindowRecordAccumulates(t *testing.T) {
	resetQuotaWindowTables(t)
	user := newUnlimitedTestUser(t, 100000, false)
	now := quotaWindowTestTime(t, "2026-11-05 09:12:34")
	start := QuotaWindowStart(model.QuotaWindowKind5h, now)

	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, start, 30))
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, start, 20))

	assert.EqualValues(t, 50, model.GetQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, start))
	var row model.QuotaWindowUsage
	require.NoError(t, model.DB.Where("user_id = ? AND window_kind = ? AND window_start = ?",
		user.Id, model.QuotaWindowKind5h, start).First(&row).Error)
	assert.EqualValues(t, 50, row.UsedQuota)
}

func TestQuotaWindowRecordClampsAtZero(t *testing.T) {
	resetQuotaWindowTables(t)
	user := newUnlimitedTestUser(t, 100000, false)
	now := quotaWindowTestTime(t, "2026-11-05 09:12:34")
	start := QuotaWindowStart(model.QuotaWindowKind7d, now)

	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, start, 40))
	require.NoError(t, model.RecordQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, start, -100))
	assert.EqualValues(t, 0, model.GetQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, start))
}

func TestQuotaWindowLimitResolution(t *testing.T) {
	resetQuotaWindowTables(t)
	user := newUnlimitedTestUser(t, 100000, false)

	// Nothing configured: unlimited.
	assert.EqualValues(t, 0, model.GetQuotaWindowLimit(user.Id, "default", model.QuotaWindowKind5h))

	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 1000))
	assert.EqualValues(t, 1000, model.GetQuotaWindowLimit(user.Id, "default", model.QuotaWindowKind5h))

	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGroup, "team-a", model.QuotaWindowKind5h, 500))
	assert.EqualValues(t, 1000, model.GetQuotaWindowLimit(user.Id, "default", model.QuotaWindowKind5h))
	assert.EqualValues(t, 500, model.GetQuotaWindowLimit(user.Id, "team-a", model.QuotaWindowKind5h))

	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeUser, "0", model.QuotaWindowKind5h, 100))
	_ = user // user scope key uses numeric id
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeUser, itoa(user.Id), model.QuotaWindowKind5h, 100))
	assert.EqualValues(t, 100, model.GetQuotaWindowLimit(user.Id, "team-a", model.QuotaWindowKind5h))

	require.NoError(t, model.DeleteQuotaWindowLimit(model.QuotaWindowScopeUser, itoa(user.Id), model.QuotaWindowKind5h))
	assert.EqualValues(t, 500, model.GetQuotaWindowLimit(user.Id, "team-a", model.QuotaWindowKind5h))
	assert.EqualValues(t, 0, model.GetQuotaWindowLimit(user.Id, "team-a", model.QuotaWindowKind7d))
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}

func TestQuotaWindowRecordedFromSettle(t *testing.T) {
	resetQuotaWindowTables(t)
	gin.SetMode(gin.TestMode)
	user := newUnlimitedTestUser(t, 100000, false)
	c := newUnlimitedTestContext()
	relayInfo := &relaycommon.RelayInfo{
		UserId:                user.Id,
		UsingGroup:            "default",
		OriginModelName:       "gpt-4o-mini",
		FinalPreConsumedQuota: 0,
	}

	// No billing session => legacy PostConsumeQuota fallback path.
	require.NoError(t, SettleBilling(c, relayInfo, 120))

	now := time.Now()
	assert.EqualValues(t, 120, model.GetQuotaWindowUsage(user.Id, model.QuotaWindowKind5h, QuotaWindowStart(model.QuotaWindowKind5h, now)))
	assert.EqualValues(t, 120, model.GetQuotaWindowUsage(user.Id, model.QuotaWindowKind7d, QuotaWindowStart(model.QuotaWindowKind7d, now)))
}

func TestQuotaWindowRecordFailureDoesNotPanic(t *testing.T) {
	resetQuotaWindowTables(t)
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })
	assert.NotPanics(t, func() {
		recordUserQuotaWindowUsage(1, "default", 100, false)
	})
}

// TestQuotaWindowDBMatrix runs the same schema/upsert checks on the dialect
// selected through TEST_QUOTA_WINDOW_DIALECT (sqlite default, mysql/postgres
// with TEST_<DIALECT>_DSN), mirroring the production SQLite/MySQL/PG matrix.
func TestQuotaWindowDBMatrix(t *testing.T) {
	dialect := os.Getenv("TEST_QUOTA_WINDOW_DIALECT")
	if dialect == "" || dialect == "sqlite" {
		resetQuotaWindowTables(t)
		runQuotaWindowDBMatrix(t)
		return
	}
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	require.NotEmpty(t, dsn, "DSN env for dialect %s", dialect)
	db, err := openQuotaWindowTestDB(dialect, dsn)
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	// Run migration twice to prove idempotency.
	require.NoError(t, db.AutoMigrate(&model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	require.NoError(t, db.AutoMigrate(&model.QuotaWindowUsage{}, &model.QuotaWindowLimit{}))
	runQuotaWindowDBMatrix(t)
}

func runQuotaWindowDBMatrix(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.QuotaWindowUsage{}).Error)
	require.NoError(t, model.DB.Where("1 = 1").Delete(&model.QuotaWindowLimit{}).Error)
	model.InvalidateQuotaWindowLimitCache()

	now := quotaWindowTestTime(t, "2026-11-05 09:12:34")
	start := QuotaWindowStart(model.QuotaWindowKind5h, now)
	require.NoError(t, model.RecordQuotaWindowUsage(99, model.QuotaWindowKind5h, start, 25))
	require.NoError(t, model.RecordQuotaWindowUsage(99, model.QuotaWindowKind5h, start, 25))
	assert.EqualValues(t, 50, model.GetQuotaWindowUsage(99, model.QuotaWindowKind5h, start))
	require.NoError(t, model.RecordQuotaWindowUsage(99, model.QuotaWindowKind5h, start, -500))
	assert.EqualValues(t, 0, model.GetQuotaWindowUsage(99, model.QuotaWindowKind5h, start))

	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 700))
	assert.EqualValues(t, 700, model.GetQuotaWindowLimit(99, "default", model.QuotaWindowKind5h))
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeUser, "99", model.QuotaWindowKind5h, 70))
	assert.EqualValues(t, 70, model.GetQuotaWindowLimit(99, "default", model.QuotaWindowKind5h))
}

func installQuotaWindowWarningRecorder(t *testing.T) *atomic.Int64 {
	t.Helper()
	previousQuotaRemind := common.QuotaRemindEnabled
	common.QuotaRemindEnabled = true
	var notifications atomic.Int64
	previousNotify := quotaWindowNotify
	quotaWindowNotify = func(userId int, email string, setting relaykitdto.UserSetting, data relaykitdto.Notify) error {
		notifications.Add(1)
		return nil
	}
	t.Cleanup(func() {
		quotaWindowNotify = previousNotify
		common.QuotaRemindEnabled = previousQuotaRemind
	})
	return &notifications
}

func TestQuotaWindowWarningFiresOnceWhenCrossingThreshold(t *testing.T) {
	resetQuotaWindowTables(t)
	user := newUnlimitedTestUser(t, 100000, false)
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	notifications := installQuotaWindowWarningRecorder(t)

	recordUserQuotaWindowUsage(user.Id, "default", 70, false)
	require.Never(t, func() bool { return notifications.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)

	recordUserQuotaWindowUsage(user.Id, "default", 15, false) // 85/100 crosses 80%
	require.Eventually(t, func() bool { return notifications.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

	recordUserQuotaWindowUsage(user.Id, "default", 5, false) // still above, no new crossing
	require.Never(t, func() bool { return notifications.Load() > 1 }, 150*time.Millisecond, 10*time.Millisecond)
}

func TestQuotaWindowWarningSkipsMissingLimitUnlimitedAndDisabled(t *testing.T) {
	notifications := installQuotaWindowWarningRecorder(t)

	// No limit configured: nothing to warn about.
	resetQuotaWindowTables(t)
	user := newUnlimitedTestUser(t, 100000, false)
	recordUserQuotaWindowUsage(user.Id, "default", 500, false)
	require.Never(t, func() bool { return notifications.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)

	// Explicit unlimited-quota user.
	require.NoError(t, model.SetQuotaWindowLimit(model.QuotaWindowScopeGlobal, "", model.QuotaWindowKind5h, 100))
	recordUserQuotaWindowUsage(user.Id, "default", 90, true)
	require.Never(t, func() bool { return notifications.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)

	// User turned quota warnings off.
	require.NoError(t, model.UpdateUserSetting(user.Id, relaykitdto.UserSetting{QuotaWarningEnabled: common.GetPointer(false)}))
	recordUserQuotaWindowUsage(user.Id, "default", 90, false)
	require.Never(t, func() bool { return notifications.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)
}

func TestQuotaWindowWarningDeduplicatesWithRedis(t *testing.T) {
	useUnlimitedTestMiniRedis(t)
	now := time.Now()
	windowStart := QuotaWindowStart(model.QuotaWindowKind5h, now)
	resetAt := QuotaWindowResetAt(model.QuotaWindowKind5h, now)

	assert.True(t, quotaWindowWarningAllowed(42, model.QuotaWindowKind5h, windowStart, resetAt, now))
	assert.False(t, quotaWindowWarningAllowed(42, model.QuotaWindowKind5h, windowStart, resetAt, now))
	assert.True(t, quotaWindowWarningAllowed(42, model.QuotaWindowKind7d, windowStart, resetAt, now))
}
