package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChannelCooldownTest(t *testing.T) {
	t.Helper()
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	channelCooldownCache.mu.Lock()
	channelCooldownCache.records = make(map[int]*channelCooldownRecord)
	channelCooldownCache.mu.Unlock()
	settings := config.GlobalConfig.Get("retry_failover_setting")
	require.NoError(t, config.UpdateConfigFromMap(settings, map[string]string{"cooldown_seconds": "60"}))
	t.Cleanup(func() {
		common.RedisEnabled = previousRedis
		require.NoError(t, config.UpdateConfigFromMap(settings, map[string]string{"cooldown_seconds": "0"}))
		channelCooldownCache.mu.Lock()
		channelCooldownCache.records = make(map[int]*channelCooldownRecord)
		channelCooldownCache.mu.Unlock()
	})
}

func TestChannelCooldownExpiresAndOnlyExtends(t *testing.T) {
	setupChannelCooldownTest(t)
	const channelID = 700001

	assert.False(t, ChannelInCooldown(channelID, "gpt-4"), "an unmarked channel is selectable")

	// Deadlines are wall-clock, so keep the margins wide enough to survive a
	// loaded test binary: the sleeps below must not be able to overshoot the
	// deadline they are meant to stay inside.
	MarkChannelCooldown(channelID, "", 400*time.Millisecond, 429, "rate limited")
	assert.True(t, ChannelInCooldown(channelID, "gpt-4"))

	// A shorter cooldown must not truncate the active one.
	MarkChannelCooldown(channelID, "", 50*time.Millisecond, 429, "rate limited")
	time.Sleep(150 * time.Millisecond)
	assert.True(t, ChannelInCooldown(channelID, "gpt-4"), "the longer cooldown still governs")

	time.Sleep(400 * time.Millisecond)
	assert.False(t, ChannelInCooldown(channelID, "gpt-4"), "the cooldown expires on its own")

	MarkChannelCooldown(channelID, "", time.Minute, 500, "upstream failed")
	require.True(t, ChannelInCooldown(channelID, "gpt-4"))
	ClearChannelCooldown(channelID)
	assert.False(t, ChannelInCooldown(channelID, "gpt-4"))
}

func TestChannelCooldownScopesToModel(t *testing.T) {
	setupChannelCooldownTest(t)
	const channelID = 700002

	MarkChannelCooldown(channelID, "gpt-4", time.Minute, 429, "rate limited")

	assert.True(t, ChannelInCooldown(channelID, "gpt-4"))
	assert.False(t, ChannelInCooldown(channelID, "claude-3"), "another model on the same channel stays selectable")
}

func TestExcludeCooledDownChannelsHonorsSetting(t *testing.T) {
	setupChannelCooldownTest(t)
	settings := config.GlobalConfig.Get("retry_failover_setting")

	MarkChannelCooldown(700003, "", time.Minute, 429, "rate limited")
	assert.Equal(t, []int{700004}, excludeCooledDownChannels([]int{700003, 700004}, "gpt-4"))

	require.NoError(t, config.UpdateConfigFromMap(settings, map[string]string{"cooldown_seconds": "0"}))
	assert.Equal(t, []int{700003, 700004}, excludeCooledDownChannels([]int{700003, 700004}, "gpt-4"),
		"turning the cooldown setting off stops filtering the candidate pool")
}
