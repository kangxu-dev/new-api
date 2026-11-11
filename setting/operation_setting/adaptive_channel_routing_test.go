package operation_setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeScheduleAllowsWeeklyWindow(t *testing.T) {
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, beijing)
	require.Equal(t, time.Monday, monday.Weekday())

	setting := AdaptiveChannelRoutingSetting{
		ProbeScheduleEnabled: true,
		ProbeScheduleDays:    []int{1},
		ProbeScheduleStart:   "09:00",
		ProbeScheduleEnd:     "10:00",
	}
	assert.True(t, setting.ProbeScheduleAllows(monday.Add(9*time.Hour)))
	assert.True(t, setting.ProbeScheduleAllows(monday.Add(9*time.Hour+30*time.Minute)))
	assert.False(t, setting.ProbeScheduleAllows(monday.Add(10*time.Hour)), "the end time is exclusive")
	assert.False(t, setting.ProbeScheduleAllows(monday.Add(25*time.Hour)), "Tuesday is not selected")
}

func TestProbeScheduleAllowsCrossMidnightOnStartDay(t *testing.T) {
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, beijing)

	setting := AdaptiveChannelRoutingSetting{
		ProbeScheduleEnabled: true,
		ProbeScheduleDays:    []int{1},
		ProbeScheduleStart:   "22:00",
		ProbeScheduleEnd:     "02:00",
	}
	assert.True(t, setting.ProbeScheduleAllows(monday.Add(23*time.Hour)))
	assert.True(t, setting.ProbeScheduleAllows(monday.Add(25*time.Hour)), "Tuesday 01:00 belongs to Monday")
	assert.False(t, setting.ProbeScheduleAllows(monday.Add(26*time.Hour)), "Tuesday 02:00 is exclusive")
	assert.False(t, setting.ProbeScheduleAllows(monday.Add(1*time.Hour)), "Monday 01:00 belongs to Sunday")
}

func TestProbeScheduleAllowsSundayToMondayBoundary(t *testing.T) {
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	sunday := time.Date(2026, 10, 25, 0, 0, 0, 0, beijing)
	require.Equal(t, time.Sunday, sunday.Weekday())

	setting := AdaptiveChannelRoutingSetting{
		ProbeScheduleEnabled: true,
		ProbeScheduleDays:    []int{7},
		ProbeScheduleStart:   "22:00",
		ProbeScheduleEnd:     "02:00",
	}
	assert.True(t, setting.ProbeScheduleAllows(sunday.Add(23*time.Hour+30*time.Minute)))
	assert.True(t, setting.ProbeScheduleAllows(sunday.Add(25*time.Hour)), "Monday 01:00 belongs to Sunday")
	assert.False(t, setting.ProbeScheduleAllows(sunday.Add(47*time.Hour+30*time.Minute)), "next Monday evening is not Sunday")
}

func TestProbeScheduleUsesBeijingTimeRegardlessOfInputLocation(t *testing.T) {
	setting := AdaptiveChannelRoutingSetting{
		ProbeScheduleEnabled: true,
		ProbeScheduleDays:    []int{1},
		ProbeScheduleStart:   "09:00",
		ProbeScheduleEnd:     "10:00",
	}
	utc := time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)
	assert.True(t, setting.ProbeScheduleAllows(utc), "01:30 UTC is Monday 09:30 in Beijing")
}

func TestProbeScheduleDisabledDoesNotRestrictAndInvalidEnabledFailsClosed(t *testing.T) {
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	monday := time.Date(2026, 10, 26, 9, 30, 0, 0, beijing)

	assert.True(t, AdaptiveChannelRoutingSetting{}.ProbeScheduleAllows(monday), "the default disabled schedule never restricts")
	assert.True(t, AdaptiveChannelRoutingSetting{
		ProbeScheduleDays: []int{8}, ProbeScheduleStart: "bad", ProbeScheduleEnd: "bad",
	}.ProbeScheduleAllows(monday), "disabled schedules ignore invalid drafts")
	assert.Error(t, AdaptiveChannelRoutingSetting{
		ProbeScheduleDays: []int{1}, ProbeScheduleStart: "bad", ProbeScheduleEnd: "",
	}.ValidateProbeSchedule(), "a nonempty malformed clock is rejected even when the other endpoint is empty and the schedule is disabled")
	assert.NoError(t, AdaptiveChannelRoutingSetting{
		ProbeScheduleDays: []int{1}, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "09:00",
	}.ValidateProbeSchedule(), "distinct clocks are only required while the restriction is enabled")

	invalid := []AdaptiveChannelRoutingSetting{
		{ProbeScheduleEnabled: true, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1, 1}, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{0}, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{8}, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "9:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "24:00", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "09:60", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "09:0", ProbeScheduleEnd: "10:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "09:00", ProbeScheduleEnd: "09:00"},
		{ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1}, ProbeScheduleStart: "", ProbeScheduleEnd: ""},
	}
	for i, setting := range invalid {
		assert.Error(t, setting.ValidateProbeSchedule(), "invalid schedule %d", i)
		assert.False(t, setting.ProbeScheduleAllows(monday), "invalid enabled schedule %d must fail closed", i)
	}
}

func TestAdaptiveChannelRoutingSettingClonesScheduleDays(t *testing.T) {
	previous := adaptiveChannelRoutingSetting
	t.Cleanup(func() { adaptiveChannelRoutingSetting = previous })
	adaptiveChannelRoutingSetting.ProbeScheduleDays = []int{1, 2}

	first := GetAdaptiveChannelRoutingSetting()
	first.ProbeScheduleDays[0] = 7
	assert.Equal(t, []int{1, 2}, GetAdaptiveChannelRoutingSetting().ProbeScheduleDays)
}

func TestProbeWindowCapacity(t *testing.T) {
	for _, test := range []struct {
		window, interval int
		want             int64
	}{
		{window: 60, interval: 300, want: 12},
		{window: 30, interval: 300, want: 6},
		{window: 30, interval: 60, want: 30},
		{window: 60, interval: 30, want: 120},
		// An interval longer than the window still yields one slot, so a
		// baseline can exist at all instead of dividing by zero or skipping.
		{window: 1, interval: 3600, want: 1},
	} {
		setting := AdaptiveChannelRoutingSetting{
			WindowMinutes:        test.window,
			ProbeIntervalSeconds: test.interval,
		}
		assert.Equal(t, test.want, setting.ProbeWindowCapacity(),
			"window=%dm interval=%ds", test.window, test.interval)
	}

	assert.Zero(t, AdaptiveChannelRoutingSetting{WindowMinutes: 0, ProbeIntervalSeconds: 60}.ProbeWindowCapacity())
	assert.Zero(t, AdaptiveChannelRoutingSetting{WindowMinutes: 60, ProbeIntervalSeconds: 0}.ProbeWindowCapacity())
}
