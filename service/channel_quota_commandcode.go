package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	commandCodeProviderID   = "commandcode"
	commandCodeProviderName = "Command Code"
	commandCodeOrigin       = "https://api.commandcode.ai"

	commandCodeCreditsPath       = commandCodeOrigin + "/alpha/billing/credits"
	commandCodeSubscriptionsPath = commandCodeOrigin + "/alpha/billing/subscriptions"
	commandCodeUsageSummaryPath  = commandCodeOrigin + "/alpha/usage/summary"
)

var commandCodeQuotaProvider = &channelQuotaProvider{
	id:      commandCodeProviderID,
	name:    commandCodeProviderName,
	hosts:   []string{"api.commandcode.ai"},
	windows: []string{"5h", "week", "month"},
	fetch:   fetchCommandCodeQuota,
}

// commandCodeCreditsResponse is GET /alpha/billing/credits. The nested objects
// are pointers so a missing window is distinguishable from a zero reading.
type commandCodeCreditsResponse struct {
	Credits      *commandCodeCredits      `json:"credits"`
	WindowLimits *commandCodeWindowLimits `json:"windowLimits"`
}

type commandCodeCredits struct {
	MonthlyCredits   float64 `json:"monthlyCredits"`
	PurchasedCredits float64 `json:"purchasedCredits"`
	FreeCredits      float64 `json:"freeCredits"`
}

type commandCodeWindowLimits struct {
	FiveHour *commandCodeWindowLimit `json:"fiveHour"`
	Weekly   *commandCodeWindowLimit `json:"weekly"`
}

type commandCodeWindowLimit struct {
	Used    *float64 `json:"used"`
	Cap     *float64 `json:"cap"`
	ResetAt *int64   `json:"resetAt"`
}

// commandCodeSubscription is the billing-period part of
// GET /alpha/billing/subscriptions. It is the only source of the monthly reset
// instant and the plan name.
type commandCodeSubscription struct {
	PlanID             string `json:"planId"`
	CurrentPeriodStart string `json:"currentPeriodStart"`
	CurrentPeriodEnd   string `json:"currentPeriodEnd"`
}

type commandCodeSubscriptionsResponse struct {
	Success bool                     `json:"success"`
	Data    *commandCodeSubscription `json:"data"`
}

// commandCodeUsageSummary is the relevant part of
// GET /alpha/usage/summary?since=<period start>. TotalCost is the credit amount
// spent in the current billing period.
type commandCodeUsageSummary struct {
	TotalCost float64 `json:"totalCost"`
}

func parseCommandCodeCredits(data []byte) (*commandCodeCreditsResponse, error) {
	var credits commandCodeCreditsResponse
	if err := common.Unmarshal(data, &credits); err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "Command Code returned an invalid credits response")
	}
	if credits.Credits == nil && credits.WindowLimits == nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "Command Code credits response has no credits or window limits")
	}
	return &credits, nil
}

func parseCommandCodeSubscription(data []byte) (*commandCodeSubscription, error) {
	var response commandCodeSubscriptionsResponse
	if err := common.Unmarshal(data, &response); err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "Command Code returned an invalid subscriptions response")
	}
	if !response.Success || response.Data == nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "Command Code subscriptions response has no active billing period")
	}
	return response.Data, nil
}

func parseCommandCodeUsageSummary(data []byte) (*commandCodeUsageSummary, error) {
	var summary commandCodeUsageSummary
	if err := common.Unmarshal(data, &summary); err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeInvalidResponse, "Command Code returned an invalid usage summary response")
	}
	return &summary, nil
}

// buildCommandCodeQuota converts the three upstream payloads into the unified
// window list. Monthly data is best-effort: without a billing period and a
// usage summary the five-hour and weekly windows are still returned.
func buildCommandCodeQuota(
	credits *commandCodeCreditsResponse,
	subscription *commandCodeSubscription,
	summary *commandCodeUsageSummary,
) *ChannelQuota {
	quota := &ChannelQuota{Windows: make([]QuotaWindow, 0, 3)}

	var windowLimits commandCodeWindowLimits
	if credits.WindowLimits != nil {
		windowLimits = *credits.WindowLimits
	}
	for _, window := range []struct {
		kind  string
		name  string
		limit *commandCodeWindowLimit
	}{
		{kind: "5h", name: "5-Hour", limit: windowLimits.FiveHour},
		{kind: "week", name: "Weekly", limit: windowLimits.Weekly},
	} {
		if window.limit == nil {
			quota.Warnings = append(quota.Warnings, window.name+" window is missing from the upstream response")
			continue
		}
		if window.limit.Used == nil || window.limit.Cap == nil || *window.limit.Cap <= 0 {
			quota.Warnings = append(quota.Warnings, window.name+" window has no usable used/cap value")
			continue
		}
		windowQuota := QuotaWindow{
			Kind:    window.kind,
			Name:    window.name,
			Percent: quotaPercent(*window.limit.Used, *window.limit.Cap),
			Used:    window.limit.Used,
			Cap:     window.limit.Cap,
		}
		if window.limit.ResetAt != nil {
			resetAt := normalizeEpochMillis(*window.limit.ResetAt)
			windowQuota.ResetsAt = &resetAt
		}
		quota.Windows = append(quota.Windows, windowQuota)
	}

	if credits.Credits != nil {
		if quota.Extras == nil {
			quota.Extras = make(map[string]any, 5)
		}
		quota.Extras["monthlyCredits"] = credits.Credits.MonthlyCredits
		quota.Extras["purchasedCredits"] = credits.Credits.PurchasedCredits
		quota.Extras["freeCredits"] = credits.Credits.FreeCredits
	}
	if subscription != nil {
		quota.Plan = subscription.PlanID
		if quota.Extras == nil {
			quota.Extras = make(map[string]any, 5)
		}
		quota.Extras["billingPeriodStart"] = subscription.CurrentPeriodStart
		quota.Extras["billingPeriodEnd"] = subscription.CurrentPeriodEnd
	}

	if subscription == nil || summary == nil {
		return quota
	}
	// monthlyCredits is what is left, not the granted amount, so the period
	// cap has to be reconstructed from spent + remaining credits.
	if credits.Credits == nil {
		quota.Warnings = append(quota.Warnings, "Monthly window is missing because the remaining credits are unknown")
		return quota
	}
	limit := summary.TotalCost + credits.Credits.MonthlyCredits + credits.Credits.PurchasedCredits + credits.Credits.FreeCredits
	if limit <= 0 {
		quota.Warnings = append(quota.Warnings, "Monthly window has no usable credits cap")
		return quota
	}
	used := summary.TotalCost
	windowQuota := QuotaWindow{
		Kind:    "month",
		Name:    "Monthly",
		Percent: quotaPercent(used, limit),
		Used:    &used,
		Cap:     &limit,
	}
	if resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(subscription.CurrentPeriodEnd)); err == nil {
		millis := resetAt.UnixMilli()
		windowQuota.ResetsAt = &millis
	} else {
		quota.Warnings = append(quota.Warnings, "Monthly window reset time is not a valid timestamp")
	}
	quota.Windows = append(quota.Windows, windowQuota)
	if quota.Extras == nil {
		quota.Extras = make(map[string]any, 5)
	}
	quota.Extras["monthlySpent"] = summary.TotalCost
	return quota
}

func fetchCommandCodeQuota(ctx context.Context, client *http.Client, key string) (*ChannelQuota, error) {
	status, body, err := getChannelQuotaUpstream(ctx, client, commandCodeCreditsPath, key)
	if err != nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeUnavailable, "Command Code quota request failed: %v", err)
	}
	if status != http.StatusOK {
		return nil, channelQuotaUpstreamStatusError(commandCodeProviderID, commandCodeProviderName, status, body)
	}
	credits, err := parseCommandCodeCredits(body)
	if err != nil {
		return nil, err
	}

	var warnings []string
	var subscription *commandCodeSubscription
	var summary *commandCodeUsageSummary

	status, body, err = getChannelQuotaUpstream(ctx, client, commandCodeSubscriptionsPath, key)
	switch {
	case err != nil:
		warnings = append(warnings, "Monthly window unavailable: the billing subscription request failed")
	case status != http.StatusOK:
		warnings = append(warnings, fmt.Sprintf("Monthly window unavailable: the billing subscription request returned HTTP %d", status))
	default:
		parsed, parseErr := parseCommandCodeSubscription(body)
		if parseErr != nil {
			warnings = append(warnings, "Monthly window unavailable: the billing subscription response was invalid")
		} else {
			subscription = parsed
		}
	}

	if subscription != nil {
		periodStart := strings.TrimSpace(subscription.CurrentPeriodStart)
		if periodStart == "" {
			warnings = append(warnings, "Monthly window unavailable: the billing period start is missing")
		} else {
			summaryURL := commandCodeUsageSummaryPath + "?since=" + url.QueryEscape(periodStart)
			status, body, err = getChannelQuotaUpstream(ctx, client, summaryURL, key)
			switch {
			case err != nil:
				warnings = append(warnings, "Monthly window unavailable: the usage summary request failed")
			case status != http.StatusOK:
				warnings = append(warnings, fmt.Sprintf("Monthly window unavailable: the usage summary request returned HTTP %d", status))
			default:
				parsed, parseErr := parseCommandCodeUsageSummary(body)
				if parseErr != nil {
					warnings = append(warnings, "Monthly window unavailable: the usage summary response was invalid")
				} else {
					summary = parsed
				}
			}
		}
	}

	quota := buildCommandCodeQuota(credits, subscription, summary)
	quota.Warnings = append(quota.Warnings, warnings...)
	return quota, nil
}
