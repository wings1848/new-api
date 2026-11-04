package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// Privacy dry-run audit APIs (issue #13 backend).

func parsePrivacyAuditInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// GetPrivacyAuditStats returns the privacy-audit dashboard aggregates.
func GetPrivacyAuditStats(c *gin.Context) {
	days := parsePrivacyAuditInt(c.Query("days"), 7)
	stats, err := model.GetPrivacyAuditStats(days)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, stats)
}

// GetPrivacyAuditEvents returns one page of recorded privacy hits.
func GetPrivacyAuditEvents(c *gin.Context) {
	page := parsePrivacyAuditInt(c.Query("page"), 1)
	pageSize := parsePrivacyAuditInt(c.Query("page_size"), 20)
	days := parsePrivacyAuditInt(c.Query("days"), 30)
	views, total, err := model.GetPrivacyAuditEvents(model.PrivacyAuditEventFilter{
		Page:     page,
		PageSize: pageSize,
		Days:     days,
		Type:     strings.TrimSpace(c.Query("type")),
		Mode:     strings.TrimSpace(c.Query("mode")),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	common.ApiSuccess(c, gin.H{
		"items":     views,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// CleanupPrivacyAuditEvents deletes privacy-audit records older than the
// requested number of days.
func CleanupPrivacyAuditEvents(c *gin.Context) {
	var request struct {
		OlderThanDays int `json:"older_than_days"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	deletedEvents, deletedFindings, err := model.CleanupPrivacyAuditEvents(request.OlderThanDays)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, 0, "privacy.audit_cleanup", map[string]any{
		"older_than_days":  request.OlderThanDays,
		"deleted_events":   deletedEvents,
		"deleted_findings": deletedFindings,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Update successful",
		"data": gin.H{
			"deleted_events":   deletedEvents,
			"deleted_findings": deletedFindings,
		},
	})
}
