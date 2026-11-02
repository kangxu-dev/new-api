package operation_setting

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting/config"
)

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
	// ProbeEnabled lets the scheduled probe task spend a small amount of
	// upstream usage to build a per-channel baseline for ProbeModel. It is off
	// by default so an upgrade never starts paid requests by itself.
	ProbeEnabled bool `json:"probe_enabled"`
	// ProbeModel is the exact client-facing model name used for probes. A
	// channel is probed only when it explicitly lists this model, and the
	// baseline is keyed by this name so changing it discards the old baseline.
	ProbeModel string `json:"probe_model"`
	// ProbeIntervalSeconds is the delay between scheduled probe rounds.
	ProbeIntervalSeconds int `json:"probe_interval_seconds"`
	// ProbeMaxRequests bounds the probes sent in one round.
	ProbeMaxRequests int `json:"probe_max_requests"`
	// ProbeMaxOutputTokens bounds the output tokens of every probe request
	// after conversion and parameter overrides.
	ProbeMaxOutputTokens int `json:"probe_max_output_tokens"`
	// ProbeScheduleEnabled restricts active probe dispatch to a weekly window.
	// It is off by default so existing probe deployments keep their behavior.
	ProbeScheduleEnabled bool `json:"probe_schedule_enabled"`
	// ProbeScheduleDays contains ISO weekdays (Monday=1 ... Sunday=7). A
	// cross-midnight window belongs to its start day.
	ProbeScheduleDays []int `json:"probe_schedule_days"`
	// ProbeScheduleStart/End are HH:mm clocks in Asia/Shanghai (UTC+8). Start is
	// inclusive and end is exclusive; equal clocks are invalid.
	ProbeScheduleStart string `json:"probe_schedule_start"`
	ProbeScheduleEnd   string `json:"probe_schedule_end"`
}

const (
	AdaptiveLatencyModeAbsolute = "absolute"
	AdaptiveLatencyModeRatio    = "ratio"

	AdaptiveProbeIntervalSecondsMin     = 30
	AdaptiveProbeIntervalSecondsMax     = 3600
	AdaptiveProbeIntervalSecondsDefault = 60
	AdaptiveProbeMaxRequestsMin         = 1
	AdaptiveProbeMaxRequestsMax         = 100
	AdaptiveProbeMaxRequestsDefault     = 6
	AdaptiveProbeMaxOutputTokensMin     = 1
	AdaptiveProbeMaxOutputTokensMax     = 1024
	AdaptiveProbeMaxOutputTokensDefault = 16
	AdaptiveProbeModelMaxLength         = 128
	AdaptiveProbeScheduleStartDefault   = "09:00"
	AdaptiveProbeScheduleEndDefault     = "23:00"
)

var defaultAdaptiveProbeScheduleDays = []int{1, 2, 3, 4, 5, 6, 7}

var adaptiveChannelRoutingSetting = AdaptiveChannelRoutingSetting{
	Enabled:              false,
	WindowMinutes:        30,
	MinSamples:           10,
	LatencyMode:          AdaptiveLatencyModeAbsolute,
	LatencyExponent:      2,
	MultiplierMin:        0.5,
	MultiplierMax:        1.5,
	ProbeEnabled:         false,
	ProbeModel:           "",
	ProbeIntervalSeconds: AdaptiveProbeIntervalSecondsDefault,
	ProbeMaxRequests:     AdaptiveProbeMaxRequestsDefault,
	ProbeMaxOutputTokens: AdaptiveProbeMaxOutputTokensDefault,
	ProbeScheduleEnabled: false,
	ProbeScheduleDays:    defaultAdaptiveProbeScheduleDays,
	ProbeScheduleStart:   AdaptiveProbeScheduleStartDefault,
	ProbeScheduleEnd:     AdaptiveProbeScheduleEndDefault,
}

func init() {
	config.GlobalConfig.Register("adaptive_channel_routing_setting", &adaptiveChannelRoutingSetting)
}

func GetAdaptiveChannelRoutingSetting() AdaptiveChannelRoutingSetting {
	setting := adaptiveChannelRoutingSetting
	setting.ProbeScheduleDays = append([]int(nil), setting.ProbeScheduleDays...)
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
	setting.ProbeModel = strings.TrimSpace(setting.ProbeModel)
	if len(setting.ProbeModel) > AdaptiveProbeModelMaxLength {
		setting.ProbeModel = ""
	}
	if setting.ProbeIntervalSeconds < AdaptiveProbeIntervalSecondsMin || setting.ProbeIntervalSeconds > AdaptiveProbeIntervalSecondsMax {
		setting.ProbeIntervalSeconds = AdaptiveProbeIntervalSecondsDefault
	}
	if setting.ProbeMaxRequests < AdaptiveProbeMaxRequestsMin || setting.ProbeMaxRequests > AdaptiveProbeMaxRequestsMax {
		setting.ProbeMaxRequests = AdaptiveProbeMaxRequestsDefault
	}
	if setting.ProbeMaxOutputTokens < AdaptiveProbeMaxOutputTokensMin || setting.ProbeMaxOutputTokens > AdaptiveProbeMaxOutputTokensMax {
		setting.ProbeMaxOutputTokens = AdaptiveProbeMaxOutputTokensDefault
	}
	return setting
}

var adaptiveProbeScheduleZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// ValidateProbeSchedule checks the weekly restriction before it is persisted or
// used. A disabled schedule may keep an incomplete draft, but any supplied value
// must still be well formed; an enabled schedule needs at least one day and two
// distinct HH:mm clocks.
func (setting AdaptiveChannelRoutingSetting) ValidateProbeSchedule() error {
	if len(setting.ProbeScheduleDays) == 0 {
		if setting.ProbeScheduleEnabled {
			return fmt.Errorf("adaptive probe schedule must select at least one day")
		}
	} else {
		seen := make(map[int]struct{}, len(setting.ProbeScheduleDays))
		for _, day := range setting.ProbeScheduleDays {
			if day < 1 || day > 7 {
				return fmt.Errorf("adaptive probe schedule day must be between 1 and 7")
			}
			if _, exists := seen[day]; exists {
				return fmt.Errorf("adaptive probe schedule day %d is duplicated", day)
			}
			seen[day] = struct{}{}
		}
	}
	if setting.ProbeScheduleStart == "" && setting.ProbeScheduleEnd == "" {
		if setting.ProbeScheduleEnabled {
			return fmt.Errorf("adaptive probe schedule start and end times are required")
		}
		return nil
	}
	start, startOK := parseProbeScheduleClock(setting.ProbeScheduleStart)
	if setting.ProbeScheduleStart != "" && !startOK {
		return fmt.Errorf("adaptive probe schedule times must use HH:mm")
	}
	end, endOK := parseProbeScheduleClock(setting.ProbeScheduleEnd)
	if setting.ProbeScheduleEnd != "" && !endOK {
		return fmt.Errorf("adaptive probe schedule times must use HH:mm")
	}
	if setting.ProbeScheduleEnabled && (setting.ProbeScheduleStart == "" || setting.ProbeScheduleEnd == "") {
		return fmt.Errorf("adaptive probe schedule start and end times are required")
	}
	if setting.ProbeScheduleEnabled && start == end {
		return fmt.Errorf("adaptive probe schedule start and end times must differ")
	}
	return nil
}

// ProbeScheduleAllows reports whether automatic probe dispatch is allowed at
// now. It always uses Asia/Shanghai and fails closed on invalid enabled data.
// Disabled schedules do not restrict anything.
func (setting AdaptiveChannelRoutingSetting) ProbeScheduleAllows(now time.Time) bool {
	if !setting.ProbeScheduleEnabled {
		return true
	}
	if setting.ValidateProbeSchedule() != nil {
		return false
	}
	start, _ := parseProbeScheduleClock(setting.ProbeScheduleStart)
	end, _ := parseProbeScheduleClock(setting.ProbeScheduleEnd)
	local := now.In(adaptiveProbeScheduleZone)
	weekday := int(local.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	minutes := local.Hour()*60 + local.Minute()
	if start < end {
		return slices.Contains(setting.ProbeScheduleDays, weekday) && minutes >= start && minutes < end
	}
	previousDay := weekday - 1
	if previousDay == 0 {
		previousDay = 7
	}
	return (slices.Contains(setting.ProbeScheduleDays, weekday) && minutes >= start) ||
		(slices.Contains(setting.ProbeScheduleDays, previousDay) && minutes < end)
}

func parseProbeScheduleClock(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	for _, index := range []int{0, 1, 3, 4} {
		if value[index] < '0' || value[index] > '9' {
			return 0, false
		}
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil || hour > 23 {
		return 0, false
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}
