package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type AdaptiveChannelRoutingSetting struct {
	Enabled       bool `json:"enabled"`
	WindowMinutes int  `json:"window_minutes"`
	MinSamples    int  `json:"min_samples"`
}

var adaptiveChannelRoutingSetting = AdaptiveChannelRoutingSetting{
	Enabled:       false,
	WindowMinutes: 30,
	MinSamples:    10,
}

func init() {
	config.GlobalConfig.Register("adaptive_channel_routing_setting", &adaptiveChannelRoutingSetting)
}

func GetAdaptiveChannelRoutingSetting() AdaptiveChannelRoutingSetting {
	setting := adaptiveChannelRoutingSetting
	if setting.WindowMinutes < 1 || setting.WindowMinutes > 60 {
		setting.WindowMinutes = 30
	}
	if setting.MinSamples < 1 {
		setting.MinSamples = 10
	}
	if setting.MinSamples > 10000 {
		setting.MinSamples = 10000
	}
	return setting
}
