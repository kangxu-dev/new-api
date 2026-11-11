package controller

import (
	"strings"

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
	candidates, err := adaptiveChannelScoreCandidates()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	groups := service.SnapshotAdaptiveChannelScores(candidates)
	for i := range groups {
		for j := range groups[i].Channels {
			channel, err := model.CacheGetChannel(groups[i].Channels[j].ChannelID)
			if err == nil && channel != nil {
				groups[i].Channels[j].ChannelName = channel.Name
			}
		}
	}
	probeBaselines := service.SnapshotAdaptiveChannelProbeScores()
	for i := range probeBaselines {
		channel, err := model.CacheGetChannel(probeBaselines[i].ChannelID)
		if err == nil && channel != nil {
			probeBaselines[i].ChannelName = channel.Name
		}
	}
	common.ApiSuccess(c, gin.H{
		"enabled":        setting.Enabled,
		"latency_mode":   setting.LatencyMode,
		"window_minutes": setting.WindowMinutes,
		"min_samples":    setting.MinSamples,
		"probe_enabled":  setting.ProbeEnabled,
		"probe_model":    setting.ProbeModel,
		// The probe task fills the window rather than stopping at MinSamples, so
		// this is the most samples a baseline can hold. When it sits below
		// min_samples the baseline can never be scored and the badge stays on
		// "sampling" forever, which is otherwise indistinguishable from a probe
		// that is merely still filling.
		"probe_window_capacity": setting.ProbeWindowCapacity(),
		"groups":                groups,
		"probe_baselines":       probeBaselines,
	})
}

func adaptiveChannelScoreCandidates() (map[string]map[string][]int, error) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return nil, err
	}
	candidates := make(map[string]map[string][]int)
	for _, channel := range channels {
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		for _, group := range channel.GetGroups() {
			if candidates[group] == nil {
				candidates[group] = make(map[string][]int)
			}
			for _, modelName := range channel.GetModels() {
				modelName = strings.TrimSpace(modelName)
				if modelName == "" {
					continue
				}
				candidates[group][modelName] = append(candidates[group][modelName], channel.Id)
			}
		}
	}
	return candidates, nil
}
