package controller

import (
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// 5h/7d quota-window administration (issue #7): admins configure per-user and
// per-group limits, users read their own windows. -1 clears a scope's override
// (falls back to the next scope), 0 means no limit at that scope.

const quotaWindowUnsetValue int64 = -1

type quotaWindowItem struct {
	Kind      string `json:"kind"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	ResetAt   int64  `json:"reset_at"`
	Source    string `json:"source"`
}

type quotaWindowOverrides struct {
	FiveHour int64 `json:"window_5h_quota"`
	SevenDay int64 `json:"window_7d_quota"`
}

type quotaWindowOverview struct {
	UserId        int                  `json:"user_id,omitempty"`
	Username      string               `json:"username,omitempty"`
	Group         string               `json:"group"`
	Windows       []quotaWindowItem    `json:"windows"`
	UserOverrides quotaWindowOverrides `json:"user_overrides"`
	ServerTime    int64                `json:"server_time"`
}

type quotaWindowUpdateRequest struct {
	Window5hQuota *int64 `json:"window_5h_quota"`
	Window7dQuota *int64 `json:"window_7d_quota"`
}

func buildQuotaWindowItem(userId int, group string, kind string, now time.Time) quotaWindowItem {
	limit, source := model.ResolveQuotaWindowLimit(userId, group, kind)
	item := quotaWindowItem{
		Kind:    kind,
		Limit:   limit,
		Source:  source,
		ResetAt: service.QuotaWindowResetAt(kind, now),
	}
	if limit > 0 {
		item.Used = model.GetQuotaWindowUsage(userId, kind, service.QuotaWindowStart(kind, now))
		item.Remaining = limit - item.Used
		if item.Remaining < 0 {
			item.Remaining = 0
		}
	}
	return item
}

func buildQuotaWindowOverview(userId int, username string, group string, now time.Time) quotaWindowOverview {
	overview := quotaWindowOverview{
		UserId:     userId,
		Username:   username,
		Group:      group,
		ServerTime: now.Unix(),
	}
	overview.Windows = []quotaWindowItem{
		buildQuotaWindowItem(userId, group, model.QuotaWindowKind5h, now),
		buildQuotaWindowItem(userId, group, model.QuotaWindowKind7d, now),
	}
	overview.UserOverrides = quotaWindowOverrides{
		FiveHour: quotaWindowOverrideValue(model.QuotaWindowScopeUser, strconv.Itoa(userId), model.QuotaWindowKind5h),
		SevenDay: quotaWindowOverrideValue(model.QuotaWindowScopeUser, strconv.Itoa(userId), model.QuotaWindowKind7d),
	}
	return overview
}

func quotaWindowOverrideValue(scopeType string, scopeKey string, kind string) int64 {
	if limit, ok := model.GetQuotaWindowLimitOverride(scopeType, scopeKey, kind); ok {
		return limit
	}
	return quotaWindowUnsetValue
}

// GetQuotaWindowSelf returns the calling user's 5h/7d usage, limit, remaining
// quota and reset time.
func GetQuotaWindowSelf(c *gin.Context) {
	userId := c.GetInt("id")
	group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if group == "" {
		if storedGroup, err := model.GetUserGroup(userId, false); err == nil {
			group = storedGroup
		}
	}
	common.ApiSuccess(c, buildQuotaWindowOverview(userId, c.GetString("username"), group, time.Now()))
}

func quotaWindowAdminTargetUser(c *gin.Context) (*model.User, bool) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil || userId <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return nil, false
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		common.ApiErrorT(c, "User does not exist")
		return nil, false
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		common.ApiErrorT(c, "No permission to update users of same or higher permission level")
		return nil, false
	}
	return user, true
}

// GetQuotaWindowAdminUser returns one user's windows for the admin console.
func GetQuotaWindowAdminUser(c *gin.Context) {
	user, ok := quotaWindowAdminTargetUser(c)
	if !ok {
		return
	}
	common.ApiSuccess(c, buildQuotaWindowOverview(user.Id, user.Username, user.Group, time.Now()))
}

// UpdateQuotaWindowAdminUser sets or clears one user's 5h/7d window limits.
func UpdateQuotaWindowAdminUser(c *gin.Context) {
	user, ok := quotaWindowAdminTargetUser(c)
	if !ok {
		return
	}
	var request quotaWindowUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	scopeKey := strconv.Itoa(user.Id)
	params := map[string]any{
		"action":   "user.quota_window_update",
		"id":       user.Id,
		"username": user.Username,
	}
	success := false
	defer func() {
		params["success"] = success
		recordManageAuditFor(c, user.Id, "user.quota_window_update", params)
	}()
	if !applyQuotaWindowUpdates(c, model.QuotaWindowScopeUser, scopeKey, request, params) {
		return
	}
	success = true
	common.ApiSuccess(c, buildQuotaWindowOverview(user.Id, user.Username, user.Group, time.Now()))
}

// GetQuotaWindowAdminGroup returns one group's effective window limits.
func GetQuotaWindowAdminGroup(c *gin.Context) {
	group := c.Param("name")
	if group == "" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	now := time.Now()
	overview := quotaWindowOverview{Group: group, ServerTime: now.Unix()}
	for _, kind := range []string{model.QuotaWindowKind5h, model.QuotaWindowKind7d} {
		limit, source := model.ResolveQuotaWindowGroupLimit(group, kind)
		overview.Windows = append(overview.Windows, quotaWindowItem{
			Kind:    kind,
			Limit:   limit,
			Source:  source,
			ResetAt: service.QuotaWindowResetAt(kind, now),
		})
	}
	overview.UserOverrides = quotaWindowOverrides{
		FiveHour: quotaWindowOverrideValue(model.QuotaWindowScopeGroup, group, model.QuotaWindowKind5h),
		SevenDay: quotaWindowOverrideValue(model.QuotaWindowScopeGroup, group, model.QuotaWindowKind7d),
	}
	common.ApiSuccess(c, overview)
}

// UpdateQuotaWindowAdminGroup sets or clears one group's 5h/7d default limits.
func UpdateQuotaWindowAdminGroup(c *gin.Context) {
	group := c.Param("name")
	if group == "" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var request quotaWindowUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	params := map[string]any{
		"action": "group.quota_window_update",
		"group":  group,
	}
	success := false
	defer func() {
		params["success"] = success
		recordManageAuditFor(c, 0, "group.quota_window_update", params)
	}()
	if !applyQuotaWindowUpdates(c, model.QuotaWindowScopeGroup, group, request, params) {
		return
	}
	success = true
	common.ApiSuccess(c, nil)
}

// applyQuotaWindowUpdates applies the non-nil fields of a window update request
// to one scope and records them in the audit params.
func applyQuotaWindowUpdates(c *gin.Context, scopeType string, scopeKey string, request quotaWindowUpdateRequest, params map[string]any) bool {
	updates := []struct {
		kind  string
		key   string
		value *int64
	}{
		{model.QuotaWindowKind5h, "window_5h_quota", request.Window5hQuota},
		{model.QuotaWindowKind7d, "window_7d_quota", request.Window7dQuota},
	}
	for _, update := range updates {
		if update.value == nil {
			continue
		}
		value := *update.value
		if value < quotaWindowUnsetValue {
			common.ApiErrorT(c, "Invalid parameters")
			return false
		}
		var err error
		if value == quotaWindowUnsetValue {
			err = model.DeleteQuotaWindowLimit(scopeType, scopeKey, update.kind)
		} else {
			err = model.SetQuotaWindowLimit(scopeType, scopeKey, update.kind, value)
		}
		if err != nil {
			common.ApiError(c, err)
			return false
		}
		params[update.key] = value
	}
	return true
}
