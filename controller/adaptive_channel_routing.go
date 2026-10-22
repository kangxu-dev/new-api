package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// GetAdaptiveChannelScores reports the adaptive routing score exactly as the
// selector sees it right now. It is read-only and exists because the multiplier
// is otherwise invisible: a score that is inert (no channel reached the sample
// threshold) and one that is applying but finds the channels equally good look
// identical from the outside.
func GetAdaptiveChannelScores(c *gin.Context) {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	groups := service.SnapshotAdaptiveChannelScores()
	for i := range groups {
		for j := range groups[i].Channels {
			channel, err := model.CacheGetChannel(groups[i].Channels[j].ChannelID)
			if err == nil && channel != nil {
				groups[i].Channels[j].ChannelName = channel.Name
			}
		}
	}
	common.ApiSuccess(c, gin.H{
		"enabled":        setting.Enabled,
		"latency_mode":   setting.LatencyMode,
		"window_minutes": setting.WindowMinutes,
		"min_samples":    setting.MinSamples,
		"groups":         groups,
	})
}
