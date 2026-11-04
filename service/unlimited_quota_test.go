package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the user-level unlimited-quota bypass (issue #4): an
// administrator can mark a user as unlimited, and that user's requests must not
// move the wallet or token quota while usage is still recorded and logged.

var unlimitedTestUserSeq atomic.Int64

func newUnlimitedTestUser(t *testing.T, quota int, unlimited bool) model.User {
	t.Helper()
	user := model.User{
		Username: fmt.Sprintf("unlimited-user-%d", unlimitedTestUserSeq.Add(1)),
		Password: "password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    quota,
		AffCode:  fmt.Sprintf("aff%d", unlimitedTestUserSeq.Load()),
	}
	require.NoError(t, model.DB.Create(&user).Error)
	if unlimited {
		require.NoError(t, model.SetUserUnlimitedQuota(user.Id, true))
	}
	return user
}

func newUnlimitedTestToken(t *testing.T, userId int) model.Token {
	t.Helper()
	token := model.Token{
		UserId:       userId,
		Key:          fmt.Sprintf("unlimited-key-%d", unlimitedTestUserSeq.Add(1)),
		Name:         "unlimited-token",
		CreatedTime:  common.GetTimestamp(),
		AccessedTime: common.GetTimestamp(),
		ExpiredTime:  -1,
		Status:       common.TokenStatusEnabled,
		RemainQuota:  0,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	return token
}

func newUnlimitedTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

func unlimitedTestRelayInfo(user model.User, token *model.Token) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{UserId: user.Id, OriginModelName: "gpt-4o-mini", UserUnlimited: true}
	if token != nil {
		info.TokenId = token.Id
		info.TokenKey = token.Key
	}
	return info
}

// TestUnlimitedQuotaWalletBypass verifies an unlimited user with a zero wallet
// balance is admitted to billing instead of failing the wallet pre-consume.
func TestUnlimitedQuotaWalletBypass(t *testing.T) {
	c := newUnlimitedTestContext()
	c.Set(string(constant.ContextKeyUserUnlimitedQuota), true)
	user := newUnlimitedTestUser(t, 0, false)

	session, apiErr := NewBillingSession(c, unlimitedTestRelayInfo(user, nil), 100)
	require.Nil(t, apiErr, "unlimited user must not be rejected for insufficient quota")
	require.NotNil(t, session)
	assert.Zero(t, session.GetPreConsumedQuota())
}

// TestUnlimitedQuotaSettleKeepsBalances verifies settling a request for an
// unlimited user leaves both the wallet quota and the token remainder unchanged.
func TestUnlimitedQuotaSettleKeepsBalances(t *testing.T) {
	c := newUnlimitedTestContext()
	user := newUnlimitedTestUser(t, 0, true)
	token := newUnlimitedTestToken(t, user.Id)

	session, apiErr := NewBillingSession(c, unlimitedTestRelayInfo(user, &token), 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(250))

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Zero(t, wallet, "wallet quota must not move for an unlimited user")

	var stored model.Token
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Zero(t, stored.RemainQuota, "token remainder must not move for an unlimited user")
}

// TestUnlimitedQuotaRefundIdempotent covers the pre-consume refund path.
func TestUnlimitedQuotaRefundIdempotent(t *testing.T) {
	c := newUnlimitedTestContext()
	user := newUnlimitedTestUser(t, 0, true)
	session, apiErr := NewBillingSession(c, unlimitedTestRelayInfo(user, nil), 100)
	require.Nil(t, apiErr)

	session.Refund(c)
	session.Refund(c)

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Zero(t, wallet)
}

// TestUnlimitedQuotaFiniteUserStillBlocked is the regression guard: users
// without the flag must still be rejected when the wallet cannot cover the
// pre-consume.
func TestUnlimitedQuotaFiniteUserStillBlocked(t *testing.T) {
	c := newUnlimitedTestContext()
	user := newUnlimitedTestUser(t, 0, false)

	_, apiErr := NewBillingSession(c, &relaycommon.RelayInfo{UserId: user.Id, OriginModelName: "gpt-4o-mini"}, 100)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
}

// TestUnlimitedQuotaContextPropagation covers the auth context -> RelayInfo path.
func TestUnlimitedQuotaContextPropagation(t *testing.T) {
	c := newUnlimitedTestContext()
	base := model.UserBase{Id: 1, UnlimitedQuota: true}
	base.WriteContext(c)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyUserUnlimitedQuota))
}

// useUnlimitedTestMiniRedis switches common.RDB to an in-memory Redis.
func useUnlimitedTestMiniRedis(t *testing.T) {
	t.Helper()
	server := miniredis.RunT(t)
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled = oldEnabled
		common.RDB = oldRDB
	})
}

// TestUnlimitedQuotaUserCacheRoundTrip guards the cache writing/reading of the
// new field: a cached unlimited user must pass the integrity MAC check instead
// of falling back to the database on every request.
func TestUnlimitedQuotaUserCacheRoundTrip(t *testing.T) {
	useUnlimitedTestMiniRedis(t)
	user := newUnlimitedTestUser(t, 0, false)
	require.NoError(t, model.SetUserUnlimitedQuota(user.Id, true))

	// First read populates the cache from the database; the second read must be
	// served by the cache and must survive the MAC integrity check.
	_, err := model.GetUserCache(user.Id)
	require.NoError(t, err)
	cached, err := model.GetUserCache(user.Id)
	require.NoError(t, err)
	assert.True(t, cached.UnlimitedQuota)

	require.NoError(t, model.SetUserUnlimitedQuota(user.Id, false))
	cached, err = model.GetUserCache(user.Id)
	require.NoError(t, err)
	assert.False(t, cached.UnlimitedQuota)
}

// TestUnlimitedQuotaLegacyPostConsumeNoCharge covers legacy settlement paths
// (WSS/audio/violation fee fallbacks) that call postConsumeQuotaWithResult.
func TestUnlimitedQuotaLegacyPostConsumeNoCharge(t *testing.T) {
	user := newUnlimitedTestUser(t, 0, true)
	token := newUnlimitedTestToken(t, user.Id)

	err := PostConsumeQuota(unlimitedTestRelayInfo(user, &token), 120, 0, false)
	require.NoError(t, err)

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Zero(t, wallet)
	var stored model.Token
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	assert.Zero(t, stored.RemainQuota)
}

// TestUnlimitedQuotaMidjourneyRefundNoop is the regression test for the refund
// blocker: an unlimited Midjourney task must not persist a refundable quota, so
// a failed task cannot credit the wallet or the token.
func TestUnlimitedQuotaMidjourneyRefundNoop(t *testing.T) {
	user := newUnlimitedTestUser(t, 0, true)
	token := newUnlimitedTestToken(t, user.Id)
	relayInfo := unlimitedTestRelayInfo(user, &token)

	task := &model.Midjourney{
		UserId:    user.Id,
		MjId:      fmt.Sprintf("mj-unlimited-%d", unlimitedTestUserSeq.Add(1)),
		Action:    "IMAGINE",
		ChannelId: 1,
	}
	require.NoError(t, model.DB.Create(task).Error)

	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, 100, true)
	require.NoError(t, err)
	require.True(t, prepared)
	applied, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)
	require.NoError(t, err)
	assert.True(t, applied, "unlimited MJ task should still be logged as consumed")

	var stored model.Midjourney
	require.NoError(t, model.DB.First(&stored, task.Id).Error)
	assert.Zero(t, stored.Quota, "refundable quota must not be persisted for an unlimited user")
	assert.Zero(t, stored.TokenId, "refundable token id must not be persisted for an unlimited user")

	refunded := RefundMidjourneyQuota(context.Background(), &stored, "task failed")
	assert.True(t, refunded)

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Zero(t, wallet, "failed unlimited MJ task must not credit the wallet")
	var tokenAfter model.Token
	require.NoError(t, model.DB.First(&tokenAfter, token.Id).Error)
	assert.Zero(t, tokenAfter.RemainQuota, "failed unlimited MJ task must not credit the token")
}

// TestUnlimitedQuotaTaskAdjustNoop verifies async-task settlement does not
// charge an unlimited user.
func TestUnlimitedQuotaTaskAdjustNoop(t *testing.T) {
	user := newUnlimitedTestUser(t, 0, true)
	task := &model.Task{UserId: user.Id, PrivateData: model.TaskPrivateData{Unlimited: true}}
	require.NoError(t, taskAdjustFunding(task, 500))

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Zero(t, wallet)
}

// TestUnlimitedQuotaLogMarker covers the consume-log marker used by the admin
// log UI and by reconciliation.
func TestUnlimitedQuotaLogMarker(t *testing.T) {
	limited := model.NewLogOther()
	appendBillingInfo(&relaycommon.RelayInfo{UserUnlimited: true, BillingSource: BillingSourceWallet}, limited)
	assert.Equal(t, true, limited.Snapshot()["unlimited_quota"])

	normal := model.NewLogOther()
	appendBillingInfo(&relaycommon.RelayInfo{BillingSource: BillingSourceWallet}, normal)
	_, exists := normal.Snapshot()["unlimited_quota"]
	assert.False(t, exists)
}

// TestUnlimitedQuotaMidjourneyPrecheck covers the legacy Midjourney HTTP-layer
// balance gate: unlimited users must not be blocked when the wallet cannot
// cover the price (reverify finding F1).
func TestUnlimitedQuotaMidjourneyPrecheck(t *testing.T) {
	assert.True(t, MidjourneyQuotaShortfall(0, 100, false), "finite user with an empty wallet must be blocked")
	assert.False(t, MidjourneyQuotaShortfall(0, 100, true), "unlimited user must not be blocked")
	assert.False(t, MidjourneyQuotaShortfall(1000, 100, false), "finite user with enough balance passes")
}

// TestUnlimitedQuotaPrepareSkipsRefundableQuota covers reverify finding F2: an
// unlimited task must not persist a refundable quota even before settlement.
func TestUnlimitedQuotaPrepareSkipsRefundableQuota(t *testing.T) {
	user := newUnlimitedTestUser(t, 0, true)
	task := &model.Midjourney{
		UserId:    user.Id,
		MjId:      fmt.Sprintf("mj-prepare-%d", unlimitedTestUserSeq.Add(1)),
		Action:    "IMAGINE",
		ChannelId: 1,
	}
	prepared, err := PrepareMidjourneyTaskBilling(unlimitedTestRelayInfo(user, nil), task, 100, true)
	require.NoError(t, err)
	assert.True(t, prepared)
	assert.Zero(t, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Zero(t, task.BillingChannelId)
}
