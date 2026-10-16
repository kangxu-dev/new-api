package service

import (
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
	err := config.UpdateConfigFromMap(
		config.GlobalConfig.Get("adaptive_channel_routing_setting"),
		map[string]string{
			"enabled":        strconv.FormatBool(setting.Enabled),
			"window_minutes": strconv.Itoa(setting.WindowMinutes),
			"min_samples":    strconv.Itoa(setting.MinSamples),
		},
	)
	require.NoError(t, err)
}

func TestAdaptiveChannelScorePenalizesServerErrorsAndSlowFirstByte(t *testing.T) {
	strong := adaptiveChannelScore(20, 20, 0, 1000, 20)
	weak := adaptiveChannelScore(20, 10, 10, 100000, 20)
	assert.Greater(t, strong, weak)
	assert.InDelta(t, 1.5, 0.5+strong/100, 0.001)
}
