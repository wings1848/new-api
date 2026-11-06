package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetChannelQuota returns the upstream quota windows of one single-key channel.
func GetChannelQuota(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid channel id")
		return
	}
	channel, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if channel == nil {
		common.ApiErrorMsg(c, "channel not found")
		return
	}

	c.Header("Cache-Control", "no-store")
	quota, err := service.FetchChannelQuota(c.Request.Context(), channel)
	if err != nil {
		var quotaErr *service.ChannelQuotaError
		if errors.As(err, &quotaErr) {
			common.ApiErrorStatus(c, http.StatusOK, quotaErr, gin.H{"code": quotaErr.Code})
			return
		}
		common.SysError(common.LogText("failed to fetch channel quota: channel_id=%d, error=%v", channelId, err))
		common.ApiErrorMsg(c, "Failed to fetch channel quota")
		return
	}
	common.ApiSuccess(c, quota)
}

// GetChannelQuotaProviders lists the channel providers that support quota
// monitoring so the console only offers the entry where it can work.
func GetChannelQuotaProviders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, service.ListChannelQuotaProviders())
}
