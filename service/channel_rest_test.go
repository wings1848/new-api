package service

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUpstreamQuotaExhausted(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"payment required", 402, "", true},
		{"openai insufficient quota", 429, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`, true},
		{"anthropic credit balance", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`, true},
		{"gemini resource exhausted", 429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`, true},
		{"chinese quota", 403, "额度已用尽，请充值", true},
		{"plain rate limit", 429, `{"error":{"message":"Rate limit reached for requests","type":"rate_limit_error"}}`, false},
		{"server error", 500, "insufficient_quota would be wrong here", false},
		{"bad request unrelated", 400, `{"error":{"message":"invalid model name"}}`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, IsUpstreamQuotaExhausted(testCase.status, testCase.body))
		})
	}
}

func TestRestExhaustedChannelKeyRestsKey(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	channel := &model.Channel{
		Name:   "rest-service-test-" + time.Now().Format("150405.000000"),
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}
	require.NoError(t, model.DB.Create(channel).Error)
	t.Cleanup(func() { model.DB.Unscoped().Delete(&model.Channel{}, channel.Id) })

	apiErr := types.NewOpenAIError(errors.New("You exceeded your current quota: insufficient_quota"), types.ErrorCodeInvalidRequest, 429)
	rested := RestExhaustedChannelKey(types.ChannelError{
		ChannelId:   channel.Id,
		ChannelName: channel.Name,
		UsingKey:    "key-b",
	}, apiErr, nil)
	require.True(t, rested)

	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.ChannelInfo.MultiKeyStatusList[1])
	assert.Greater(t, loaded.ChannelInfo.MultiKeyRestUntil[1], common.GetTimestamp())

	// A plain rate-limit 429 must not rest a key.
	rateErr := types.NewOpenAIError(errors.New("Rate limit reached for requests"), types.ErrorCodeInvalidRequest, 429)
	channelError := types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, UsingKey: "key-a"}
	assert.False(t, RestExhaustedChannelKey(channelError, rateErr, nil))
}
