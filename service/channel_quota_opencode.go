package service

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	openCodeGoProviderID   = "opencode-go"
	openCodeGoProviderName = "OpenCode Go"
	openCodeGoOrigin       = "https://opencode.ai"
	openCodeGoUsagePath    = openCodeGoOrigin + "/zen/go/v1/usage"
)

var openCodeGoQuotaProvider = &channelQuotaProvider{
	id:      openCodeGoProviderID,
	name:    openCodeGoProviderName,
	hosts:   []string{"opencode.ai"},
	windows: []string{"5h", "week", "month"},
	fetch:   fetchOpenCodeGoQuota,
}

// openCodeUsageWindow is one window of GET /zen/go/v1/usage. OpenCode only
// reports a used percentage and a reset instant, never an absolute amount.
type openCodeUsageWindow struct {
	Status   string   `json:"status"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resetsAt"`
}

type openCodeUsageResponse struct {
	Usage *struct {
		Rolling *openCodeUsageWindow `json:"rolling"`
		Weekly  *openCodeUsageWindow `json:"weekly"`
		Monthly *openCodeUsageWindow `json:"monthly"`
	} `json:"usage"`
}

type openCodeErrorEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

func parseOpenCodeGoUsage(data []byte) (*ChannelQuota, error) {
	var response openCodeUsageResponse
	if err := common.Unmarshal(data, &response); err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "OpenCode Go returned an invalid usage response")
	}
	if response.Usage == nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "OpenCode Go usage response has no usage windows")
	}

	quota := &ChannelQuota{Windows: make([]QuotaWindow, 0, 3)}
	for _, window := range []struct {
		kind  string
		name  string
		value *openCodeUsageWindow
	}{
		{kind: "5h", name: "5-Hour", value: response.Usage.Rolling},
		{kind: "week", name: "Weekly", value: response.Usage.Weekly},
		{kind: "month", name: "Monthly", value: response.Usage.Monthly},
	} {
		if window.value == nil {
			quota.Warnings = append(quota.Warnings, window.name+" window is missing from the upstream response")
			continue
		}
		if window.value.Percent == nil {
			quota.Warnings = append(quota.Warnings, window.name+" window has no usage percentage")
			continue
		}
		if window.value.Status != "" && window.value.Status != "ok" {
			quota.Warnings = append(quota.Warnings, window.name+" window status: "+window.value.Status)
		}
		windowQuota := QuotaWindow{
			Kind:    window.kind,
			Name:    window.name,
			Percent: clampQuotaPercent(*window.value.Percent),
		}
		if window.value.ResetsAt != "" {
			if resetAt, err := time.Parse(time.RFC3339, window.value.ResetsAt); err == nil {
				millis := resetAt.UnixMilli()
				windowQuota.ResetsAt = &millis
			} else {
				quota.Warnings = append(quota.Warnings, window.name+" window reset time is not a valid timestamp")
			}
		}
		quota.Windows = append(quota.Windows, windowQuota)
	}
	if len(quota.Windows) == 0 {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "OpenCode Go usage response has no usable windows")
	}
	return quota, nil
}

func fetchOpenCodeGoQuota(ctx context.Context, client *http.Client, key string) (*ChannelQuota, error) {
	status, body, err := getChannelQuotaUpstream(ctx, client, openCodeGoUsagePath, key)
	if err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeUnavailable, "OpenCode Go quota request failed: %v", err)
	}
	if status != http.StatusOK {
		return nil, channelQuotaUpstreamStatusError(openCodeGoProviderID, openCodeGoProviderName, status, body)
	}
	return parseOpenCodeGoUsage(body)
}
