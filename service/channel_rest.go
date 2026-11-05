package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// Upstream quota-exhaustion rotation (issue #33): when a multi-key channel's key
// reports that its account has run out of quota, rest that key instead of
// disabling the whole channel, so the relay rotates to another key. The ladder
// and lazy restore live in model.Channel.RestKey / GetNextEnabledKey.

// upstreamQuotaExhaustedMarkers are provider error fragments that mean "this
// account is out of quota/credit" rather than "slow down" (rate limit).
var upstreamQuotaExhaustedMarkers = []string{
	"insufficient_quota",
	"insufficient quota",
	"exceeded your current quota",
	"quota exhausted",
	"quota has been exhausted",
	"quota_exceeded",
	"resource_exhausted",
	"credit balance is too low",
	"billing hard limit",
	"out of credit",
	"余额不足",
	"额度已用尽",
	"配额不足",
	"额度不足",
}

// IsUpstreamQuotaExhausted reports whether an upstream error means the account
// behind the key ran out of quota. 402 always means payment required; 400/403/429
// only when the message carries a quota marker (a plain 429 is a rate limit and
// must not rest a key).
func IsUpstreamQuotaExhausted(statusCode int, body string) bool {
	if statusCode == 402 {
		return true
	}
	switch statusCode {
	case 400, 403, 429:
	default:
		return false
	}
	lower := strings.ToLower(body)
	for _, marker := range upstreamQuotaExhaustedMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// RestExhaustedChannelKey rests the failed key of a multi-key channel when the
// upstream error is a quota exhaustion. It returns true when the key was rested
// (callers must then skip disabling the whole channel).
func RestExhaustedChannelKey(channelError types.ChannelError, err *types.NewAPIError, upstreamURLs []string) bool {
	if err == nil || channelError.ChannelId <= 0 || channelError.UsingKey == "" {
		return false
	}
	masked := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
	if !IsUpstreamQuotaExhausted(err.StatusCode, masked) {
		return false
	}
	reason := "upstream quota exhausted: " + common.LocalLogPreview(masked)
	until, rested, restErr := model.RestChannelKey(channelError.ChannelId, channelError.UsingKey, reason)
	if restErr != nil {
		common.SysLog(common.LogText("failed to rest channel #%d key after quota exhaustion: %v", channelError.ChannelId, restErr))
		return false
	}
	if !rested {
		return false
	}
	common.SysLog(common.LogText("channel %q (#%d) key rested after upstream quota exhaustion until %d", channelError.ChannelName, channelError.ChannelId, until))
	return true
}
