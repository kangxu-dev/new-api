package service

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptiveChannelScoringUsesRecentScopedOutcomes(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() {
		setAdaptiveChannelRoutingSetting(t, previous)
		adaptiveChannelWindows.Lock()
		adaptiveChannelWindows.windows = make(map[adaptiveChannelKey]*adaptiveChannelWindow)
		adaptiveChannelWindows.lastPruneAt = 0
		adaptiveChannelWindows.Unlock()
	})
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
	})
	adaptiveChannelWindows.Lock()
	adaptiveChannelWindows.windows = make(map[adaptiveChannelKey]*adaptiveChannelWindow)
	adaptiveChannelWindows.lastPruneAt = 0
	adaptiveChannelWindows.Unlock()

	for range 2 {
		RecordAdaptiveChannelOutcome("codex", "gpt-test", 1, 504, false, 0, false)
		RecordAdaptiveChannelOutcome("codex", "gpt-test", 2, 200, true, 250, true)
	}

	multipliers := AdaptiveChannelWeightMultipliers("codex", "gpt-test")
	require.Len(t, multipliers, 2)
	assert.Less(t, multipliers[1], multipliers[2])
	assert.Nil(t, AdaptiveChannelWeightMultipliers("other", "gpt-test"))
	assert.Nil(t, AdaptiveChannelWeightMultipliers("codex", "other-model"))
}

func TestAdaptiveChannelScoringLeavesUnderSampledAndDisabledChannelsNeutral(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	adaptiveChannelWindows.Lock()
	adaptiveChannelWindows.windows = make(map[adaptiveChannelKey]*adaptiveChannelWindow)
	adaptiveChannelWindows.lastPruneAt = 0
	adaptiveChannelWindows.Unlock()

	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 1, MinSamples: 2,
	})
	RecordAdaptiveChannelOutcome("codex", "gpt-test", 1, 504, false, 0, false)
	assert.Nil(t, AdaptiveChannelWeightMultipliers("codex", "gpt-test"))

	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: false, WindowMinutes: 30, MinSamples: 1,
	})
	RecordAdaptiveChannelOutcome("codex", "gpt-test", 1, 200, true, time.Second.Milliseconds(), true)
	assert.Nil(t, AdaptiveChannelWeightMultipliers("codex", "gpt-test"))
}

func setAdaptiveChannelRoutingSetting(t *testing.T, setting operation_setting.AdaptiveChannelRoutingSetting) {
	t.Helper()
	values := map[string]string{
		"enabled":        strconv.FormatBool(setting.Enabled),
		"window_minutes": strconv.Itoa(setting.WindowMinutes),
		"min_samples":    strconv.Itoa(setting.MinSamples),
	}
	if setting.LatencyMode != "" {
		values["latency_mode"] = setting.LatencyMode
	}
	if setting.LatencyExponent > 0 {
		values["latency_exponent"] = strconv.FormatFloat(setting.LatencyExponent, 'f', -1, 64)
	}
	if setting.MultiplierMin > 0 {
		values["multiplier_min"] = strconv.FormatFloat(setting.MultiplierMin, 'f', -1, 64)
	}
	if setting.MultiplierMax > 0 {
		values["multiplier_max"] = strconv.FormatFloat(setting.MultiplierMax, 'f', -1, 64)
	}
	err := config.UpdateConfigFromMap(
		config.GlobalConfig.Get("adaptive_channel_routing_setting"),
		values,
	)
	require.NoError(t, err)
}

// recordAdaptiveLatency feeds n successful observations with the given average
// first byte, which is what the ratio score compares channels on.
func recordAdaptiveLatency(t *testing.T, group, model string, channelID int, n int, firstByteMs int64) {
	t.Helper()
	for range n {
		RecordAdaptiveChannelOutcome(group, model, channelID, http.StatusOK, true, firstByteMs, true)
	}
}

func resetAdaptiveWindows(t *testing.T) {
	t.Helper()
	adaptiveChannelWindows.Lock()
	adaptiveChannelWindows.windows = make(map[adaptiveChannelKey]*adaptiveChannelWindow)
	adaptiveChannelWindows.lastPruneAt = 0
	adaptiveChannelWindows.Unlock()
}

func TestAdaptiveRatioModePrefersTheFasterChannel(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	resetAdaptiveWindows(t)
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		LatencyMode: operation_setting.AdaptiveLatencyModeRatio, LatencyExponent: 2,
		MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	// The production numbers that motivated this mode: 6233ms vs 7638ms, an 18%
	// gap that the legacy absolute curve turned into a 0.3% traffic difference.
	recordAdaptiveLatency(t, "codex", "gpt-test", 1, 10, 7638)
	recordAdaptiveLatency(t, "codex", "gpt-test", 2, 10, 6233)

	multipliers := AdaptiveChannelWeightMultipliers("codex", "gpt-test")
	require.Len(t, multipliers, 2)

	// The fastest channel takes the top of the band; the slower one falls off as
	// (6233/7638)^2 = 0.666 of it.
	assert.InDelta(t, 1.5, multipliers[2], 0.001, "the fastest channel reaches the top of the band")
	assert.InDelta(t, 1.5*0.666, multipliers[1], 0.01)

	share := multipliers[2] / (multipliers[1] + multipliers[2])
	assert.InDelta(t, 0.60, share, 0.01, "an 18% first-byte advantage should be worth about 60/40, not 50/50")
}

func TestAdaptiveRatioModeLetsErrorsWeighAgainstSpeed(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	resetAdaptiveWindows(t)
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		LatencyMode: operation_setting.AdaptiveLatencyModeRatio, LatencyExponent: 2,
		MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	// Same latency on both, but channel 1 fails a quarter of its requests. The
	// error term multiplies the latency term, so the failing channel is
	// deprioritised by exactly its health factor.
	recordAdaptiveLatency(t, "codex", "gpt-test", 1, 30, 2000)
	for range 10 {
		RecordAdaptiveChannelOutcome("codex", "gpt-test", 1, http.StatusInternalServerError, false, 0, false)
	}
	recordAdaptiveLatency(t, "codex", "gpt-test", 2, 40, 2000)

	multipliers := AdaptiveChannelWeightMultipliers("codex", "gpt-test")
	require.Len(t, multipliers, 2)
	assert.InDelta(t, 1.5, multipliers[2], 0.001, "the flawless channel reaches the top of the band")
	assert.InDelta(t, 1.5*0.75, multipliers[1], 0.001, "a 25% failure rate costs a quarter of the weight")
}

func TestAdaptiveRatioModeClampsToTheConfiguredBand(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	resetAdaptiveWindows(t)
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		LatencyMode: operation_setting.AdaptiveLatencyModeRatio, LatencyExponent: 4,
		MultiplierMin: 0.2, MultiplierMax: 1.8,
	})

	// A 10x latency gap with exponent 4 would score 1e-4 of the best channel; the
	// band has to hold it up rather than let the channel starve.
	recordAdaptiveLatency(t, "codex", "gpt-test", 1, 5, 1000)
	recordAdaptiveLatency(t, "codex", "gpt-test", 2, 5, 10000)

	multipliers := AdaptiveChannelWeightMultipliers("codex", "gpt-test")
	require.Len(t, multipliers, 2)
	assert.InDelta(t, 1.8, multipliers[1], 0.001)
	assert.InDelta(t, 0.2, multipliers[2], 0.001, "the clamp keeps a slow channel from being starved entirely")
}

func TestAdaptiveRatioModeStaysNeutralWithoutLatencySamples(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	resetAdaptiveWindows(t)
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		LatencyMode: operation_setting.AdaptiveLatencyModeRatio, LatencyExponent: 2,
		MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	// Both channels only ever produced failures, so there is no latency to rank
	// on. The score must not invent a preference.
	for range 5 {
		RecordAdaptiveChannelOutcome("codex", "gpt-test", 1, http.StatusInternalServerError, false, 0, false)
		RecordAdaptiveChannelOutcome("codex", "gpt-test", 2, http.StatusInternalServerError, false, 0, false)
	}
	assert.Nil(t, AdaptiveChannelWeightMultipliers("codex", "gpt-test"))
}

func TestAdaptiveChannelScorePenalizesServerErrorsAndSlowFirstByte(t *testing.T) {
	strong := adaptiveChannelScore(20, 20, 0, 1000, 20)
	weak := adaptiveChannelScore(20, 10, 10, 100000, 20)
	assert.Greater(t, strong, weak)

	// A flawless channel with no measurable first byte reaches the top of the
	// band; the multiplier is 0.5 + score/100, so the band is [0.5, 1.5].
	perfect := adaptiveChannelScore(20, 20, 0, 0, 20)
	assert.InDelta(t, 1.5, 0.5+perfect/100, 0.001)

	// 50ms average first byte over 20 samples is good but not flawless: the
	// first-byte term is 100/(1+50/2000) = 97.56, so the blended score is
	// 0.8*100 + 0.2*97.56 and the multiplier lands just below the cap.
	assert.InDelta(t, 1.4951, 0.5+strong/100, 0.0001)

	for _, score := range []float64{strong, weak, perfect} {
		assert.GreaterOrEqual(t, score, 0.0)
		assert.LessOrEqual(t, score, 100.0)
	}
}

func TestSnapshotAdaptiveChannelScoresMarksUnscoredChannels(t *testing.T) {
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { setAdaptiveChannelRoutingSetting(t, previous) })
	resetAdaptiveWindows(t)
	setAdaptiveChannelRoutingSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 5,
		LatencyMode: operation_setting.AdaptiveLatencyModeRatio, LatencyExponent: 2,
		MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	// Channel 1 clears the sample threshold, channel 2 does not. The snapshot has
	// to say which is which: an unscored channel silently keeps its configured
	// weight, which is indistinguishable from a score that is not running.
	recordAdaptiveLatency(t, "codex", "gpt-test", 1, 6, 1000)
	recordAdaptiveLatency(t, "codex", "gpt-test", 2, 2, 500)

	reports := SnapshotAdaptiveChannelScores()
	require.Len(t, reports, 1)
	assert.Equal(t, "codex", reports[0].Group)
	assert.Equal(t, "gpt-test", reports[0].Model)
	require.Len(t, reports[0].Channels, 2)

	byID := make(map[int]AdaptiveChannelScoreSnapshot, 2)
	for _, channel := range reports[0].Channels {
		byID[channel.ChannelID] = channel
	}

	assert.True(t, byID[1].Scored)
	assert.InDelta(t, 1.5, byID[1].Multiplier, 0.001)
	assert.EqualValues(t, 6, byID[1].Requests)
	assert.InDelta(t, 1000, byID[1].AvgFirstByteMs, 0.001)

	assert.False(t, byID[2].Scored, "a channel below the sample threshold carries no multiplier")
	assert.Zero(t, byID[2].Multiplier)
	assert.EqualValues(t, 2, byID[2].Requests, "its raw counts are still reported")
}
