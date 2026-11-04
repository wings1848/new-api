package controller

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Window-dimension usage APIs (issue #31 backend).

type usageWindowStat struct {
	Kind                    string `json:"kind"`
	Limit                   int64  `json:"limit"`
	Used                    int64  `json:"used"`
	Remaining               int64  `json:"remaining"`
	ResetAt                 int64  `json:"reset_at"`
	Source                  string `json:"source"`
	EstimatedExhaustSeconds int64  `json:"estimated_exhaust_seconds"`
}

// GetUsageWindowSummary returns the caller's current 5h/7d usage, limits and a
// projected exhaustion time based on recent spend.
func GetUsageWindowSummary(c *gin.Context) {
	userId := c.GetInt("id")
	group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if group == "" {
		if storedGroup, err := model.GetUserGroup(userId, false); err == nil {
			group = storedGroup
		}
	}
	now := time.Now()
	windows := make([]usageWindowStat, 0, 2)
	for _, kind := range []string{model.QuotaWindowKind5h, model.QuotaWindowKind7d} {
		limit, source := model.ResolveQuotaWindowLimit(userId, group, kind)
		stat := usageWindowStat{
			Kind:    kind,
			Limit:   limit,
			Source:  source,
			ResetAt: service.QuotaWindowResetAt(kind, now),
		}
		if limit > 0 {
			stat.Used = model.GetQuotaWindowUsage(userId, kind, service.QuotaWindowStart(kind, now))
			stat.Remaining = limit - stat.Used
			if stat.Remaining < 0 {
				stat.Remaining = 0
			}
			stat.EstimatedExhaustSeconds = service.EstimateUserWindowExhaustion(userId, stat.Remaining, now)
		}
		windows = append(windows, stat)
	}
	common.ApiSuccess(c, gin.H{
		"windows":     windows,
		"server_time": now.Unix(),
	})
}

// GetUsageWindowReport aggregates consume logs per fixed window for admins.
func GetUsageWindowReport(c *gin.Context) {
	windowKind := c.Query("window")
	if windowKind == "" {
		windowKind = model.QuotaWindowKind5h
	}
	if windowKind != model.QuotaWindowKind5h && windowKind != model.QuotaWindowKind7d {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	groupBy := c.Query("group_by")
	if groupBy == "" {
		groupBy = "group"
	}
	if groupBy != "group" && groupBy != "model" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	days := parsePrivacyAuditInt(c.Query("days"), 7)
	report, err := service.BuildUsageWindowReport(windowKind, days, groupBy)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, report)
}
