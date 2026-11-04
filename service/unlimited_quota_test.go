package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This file covers the user-level unlimited-quota bypass (issue #4): an
// administrator can mark a user as unlimited, and that user's requests must not
// move the wallet or token quota while the usage is still logged.

var unlimitedTestUserSeq atomic.Int64

func newUnlimitedTestUser(t *testing.T, quota int) model.User {
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
	return user
}

func newUnlimitedTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

// TestUnlimitedQuotaWalletBypass verifies an unlimited user with a zero wallet
// balance is admitted to billing instead of failing the wallet pre-consume.
func TestUnlimitedQuotaWalletBypass(t *testing.T) {
	c := newUnlimitedTestContext()
	c.Set("user_unlimited_quota", true)
	user := newUnlimitedTestUser(t, 0)
	relayInfo := &relaycommon.RelayInfo{UserId: user.Id, OriginModelName: "gpt-4o-mini"}

	session, apiErr := NewBillingSession(c, relayInfo, 100)
	if apiErr != nil {
		t.Fatalf("unlimited user must not be rejected for insufficient quota, got: %s", apiErr.Error())
	}
	require.NotNil(t, session)
}

// TestUnlimitedQuotaSettleKeepsBalances verifies settling a request for an
// unlimited user leaves both the wallet quota and the token remainder unchanged.
func TestUnlimitedQuotaSettleKeepsBalances(t *testing.T) {
	c := newUnlimitedTestContext()
	c.Set("user_unlimited_quota", true)
	user := newUnlimitedTestUser(t, 0)
	token := model.Token{
		UserId:       user.Id,
		Key:          fmt.Sprintf("sk-unlimited-%d", unlimitedTestUserSeq.Add(1)),
		Name:         "unlimited-token",
		CreatedTime:  common.GetTimestamp(),
		AccessedTime: common.GetTimestamp(),
		RemainQuota:  0,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	relayInfo := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, OriginModelName: "gpt-4o-mini"}

	session, apiErr := NewBillingSession(c, relayInfo, 100)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(250))

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, 0, wallet, "wallet quota must not move for an unlimited user")

	var stored model.Token
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	require.Equal(t, 0, stored.RemainQuota, "token remainder must not move for an unlimited user")
}

// TestUnlimitedQuotaFiniteUserStillBlocked is the regression guard: users
// without the flag must still be rejected when the wallet cannot cover the
// pre-consume.
func TestUnlimitedQuotaFiniteUserStillBlocked(t *testing.T) {
	c := newUnlimitedTestContext()
	user := newUnlimitedTestUser(t, 0)
	relayInfo := &relaycommon.RelayInfo{UserId: user.Id, OriginModelName: "gpt-4o-mini"}

	_, apiErr := NewBillingSession(c, relayInfo, 100)
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
}

// TestUnlimitedQuotaSchemaFieldsExist guards the data-model shape the rest of
// the feature depends on (user column, cache projection, relay flag, task
// snapshot flag).
func TestUnlimitedQuotaSchemaFieldsExist(t *testing.T) {
	requireBoolField := func(t *testing.T, typ reflect.Type, name string) {
		t.Helper()
		field, ok := typ.FieldByName(name)
		require.Truef(t, ok, "%s must have a %s field", typ, name)
		require.Equalf(t, reflect.Bool, field.Type.Kind(), "%s.%s must be a bool", typ, name)
	}
	requireBoolField(t, reflect.TypeOf(model.User{}), "UnlimitedQuota")
	requireBoolField(t, reflect.TypeOf(model.UserBase{}), "UnlimitedQuota")
	requireBoolField(t, reflect.TypeOf(relaycommon.RelayInfo{}), "UserUnlimited")
	requireBoolField(t, reflect.TypeOf(model.TaskPrivateData{}), "Unlimited")
}

// TestUnlimitedQuotaTaskAdjustNoop verifies async-task settlement does not
// charge an unlimited user.
func TestUnlimitedQuotaTaskAdjustNoop(t *testing.T) {
	privateData := model.TaskPrivateData{}
	field := reflect.ValueOf(&privateData).Elem().FieldByName("Unlimited")
	require.True(t, field.IsValid(), "model.TaskPrivateData.Unlimited missing")
	field.SetBool(true)

	user := newUnlimitedTestUser(t, 0)
	task := &model.Task{UserId: user.Id, PrivateData: privateData}
	require.NoError(t, taskAdjustFunding(task, 500))

	wallet, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, 0, wallet, "task adjustment must not move wallet quota for an unlimited user")
}
