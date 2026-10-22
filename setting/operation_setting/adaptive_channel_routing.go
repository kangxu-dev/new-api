package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// AdaptiveChannelRoutingSetting controls the optional score that reweights
// same-priority channels by their recent behaviour.
//
// LatencyMode selects how the first-byte term is turned into a multiplier:
//
//   - "absolute" (default): the legacy curve 100/(1 + ms/2000), blended with the
//     error term at a fixed 80/20. It was tuned for sub-second first bytes and
//     saturates well before the multi-second range, so on slow upstreams a large
//     latency gap barely moves the multiplier.
//   - "ratio": every sampled channel is scored relative to the fastest one for
//     the same group and model, so no absolute reference has to be configured
//     per deployment. A channel at the fastest observed latency gets the top of
//     the band; slower ones fall off as (fastest/ms)^LatencyExponent.
type AdaptiveChannelRoutingSetting struct {
	Enabled       bool `json:"enabled"`
	WindowMinutes int  `json:"window_minutes"`
	MinSamples    int  `json:"min_samples"`
	// LatencyMode is "absolute" or "ratio"; anything else falls back to
	// "absolute" so an unknown value cannot silently change routing.
	LatencyMode string `json:"latency_mode"`
	// LatencyExponent is the ratio mode's k. Larger values prefer the fastest
	// channel more aggressively, at the cost of reacting to latency noise.
	LatencyExponent float64 `json:"latency_exponent"`
	// MultiplierMin/Max bound the weight multiplier. The default band [0.5, 1.5]
	// is a nudge; widening it is what makes the preference decisive.
	MultiplierMin float64 `json:"multiplier_min"`
	MultiplierMax float64 `json:"multiplier_max"`
}

const (
	AdaptiveLatencyModeAbsolute = "absolute"
	AdaptiveLatencyModeRatio    = "ratio"
)

var adaptiveChannelRoutingSetting = AdaptiveChannelRoutingSetting{
	Enabled:         false,
	WindowMinutes:   30,
	MinSamples:      10,
	LatencyMode:     AdaptiveLatencyModeAbsolute,
	LatencyExponent: 2,
	MultiplierMin:   0.5,
	MultiplierMax:   1.5,
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
	if setting.LatencyMode != AdaptiveLatencyModeRatio {
		setting.LatencyMode = AdaptiveLatencyModeAbsolute
	}
	if setting.LatencyExponent < 0.1 || setting.LatencyExponent > 10 {
		setting.LatencyExponent = 2
	}
	if setting.MultiplierMin < 0 || setting.MultiplierMin > 1 {
		setting.MultiplierMin = 0.5
	}
	if setting.MultiplierMax < 1 || setting.MultiplierMax > 10 {
		setting.MultiplierMax = 1.5
	}
	if setting.MultiplierMax < setting.MultiplierMin {
		setting.MultiplierMax = setting.MultiplierMin
	}
	return setting
}
