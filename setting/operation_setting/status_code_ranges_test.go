package operation_setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseHTTPStatusCodeRanges_CommaSeparated(t *testing.T) {
	ranges, err := ParseHTTPStatusCodeRanges("401,403,500-599")
	require.NoError(t, err)
	require.Equal(t, []StatusCodeRange{
		{Start: 401, End: 401},
		{Start: 403, End: 403},
		{Start: 500, End: 599},
	}, ranges)
}

func TestParseHTTPStatusCodeRanges_MergeAndNormalize(t *testing.T) {
	ranges, err := ParseHTTPStatusCodeRanges("500-505,504,401,403,402")
	require.NoError(t, err)
	require.Equal(t, []StatusCodeRange{
		{Start: 401, End: 403},
		{Start: 500, End: 505},
	}, ranges)
}

func TestParseHTTPStatusCodeRanges_Invalid(t *testing.T) {
	_, err := ParseHTTPStatusCodeRanges("99,600,foo,500-400,500-")
	require.Error(t, err)
}

func TestParseHTTPStatusCodeRanges_NoComma_IsInvalid(t *testing.T) {
	_, err := ParseHTTPStatusCodeRanges("401 403")
	require.Error(t, err)
}

func TestShouldDisableByStatusCode(t *testing.T) {
	orig := AutomaticDisableStatusCodeRanges
	t.Cleanup(func() { AutomaticDisableStatusCodeRanges = orig })

	AutomaticDisableStatusCodeRanges = []StatusCodeRange{
		{Start: 401, End: 403},
		{Start: 500, End: 599},
	}

	require.True(t, ShouldDisableByStatusCode(401))
	require.True(t, ShouldDisableByStatusCode(403))
	require.False(t, ShouldDisableByStatusCode(404))
	require.True(t, ShouldDisableByStatusCode(500))
	require.False(t, ShouldDisableByStatusCode(200))
}

func TestShouldRetryByStatusCode(t *testing.T) {
	orig := AutomaticRetryStatusCodeRanges
	t.Cleanup(func() { AutomaticRetryStatusCodeRanges = orig })

	AutomaticRetryStatusCodeRanges = []StatusCodeRange{
		{Start: 429, End: 429},
		{Start: 500, End: 599},
	}

	require.True(t, ShouldRetryByStatusCode(429))
	require.True(t, ShouldRetryByStatusCode(500))
	require.False(t, ShouldRetryByStatusCode(504))
	require.False(t, ShouldRetryByStatusCode(524))
	require.False(t, ShouldRetryByStatusCode(400))
	require.False(t, ShouldRetryByStatusCode(200))
}

func TestShouldRetryByStatusCode_DefaultMatchesLegacyBehavior(t *testing.T) {
	require.False(t, ShouldRetryByStatusCode(200))
	require.False(t, ShouldRetryByStatusCode(400))
	require.True(t, ShouldRetryByStatusCode(401))
	require.False(t, ShouldRetryByStatusCode(408))
	require.True(t, ShouldRetryByStatusCode(429))
	require.True(t, ShouldRetryByStatusCode(500))
	require.False(t, ShouldRetryByStatusCode(504))
	require.False(t, ShouldRetryByStatusCode(524))
	require.True(t, ShouldRetryByStatusCode(599))
}

func TestIsAlwaysSkipRetryStatusCode(t *testing.T) {
	require.True(t, IsAlwaysSkipRetryStatusCode(504))
	require.True(t, IsAlwaysSkipRetryStatusCode(524))
	require.False(t, IsAlwaysSkipRetryStatusCode(500))
}

func TestSameChannelRetryBackoffDoublesAndCaps(t *testing.T) {
	previous := retryFailoverSetting
	t.Cleanup(func() { retryFailoverSetting = previous })
	retryFailoverSetting.BackoffMs = 500
	retryFailoverSetting.BackoffMaxMs = 8000

	require.Equal(t, 500*time.Millisecond, SameChannelRetryBackoff(0))
	require.Equal(t, 500*time.Millisecond, SameChannelRetryBackoff(1))
	require.Equal(t, time.Second, SameChannelRetryBackoff(2))
	require.Equal(t, 2*time.Second, SameChannelRetryBackoff(3))
	require.Equal(t, 4*time.Second, SameChannelRetryBackoff(4))
	require.Equal(t, 8*time.Second, SameChannelRetryBackoff(5))
	require.Equal(t, 8*time.Second, SameChannelRetryBackoff(9), "the cap holds for a high retry budget")

	retryFailoverSetting.BackoffMs = 0
	require.Zero(t, SameChannelRetryBackoff(3), "a zero base disables the wait")
}

func TestSameChannelRetryStatusCodesDefaults(t *testing.T) {
	require.True(t, IsSameChannelRetryStatusCode(429))
	require.True(t, IsSameChannelRetryStatusCode(503))
	require.True(t, IsSameChannelRetryStatusCode(529))
	require.False(t, IsSameChannelRetryStatusCode(401), "credential failures switch channel instead")
	require.False(t, IsSameChannelRetryStatusCode(400))
	require.False(t, IsSameChannelRetryStatusCode(504), "an upstream timeout is not worth retrying in place")

	require.True(t, IsCooldownStatusCode(429))
	require.True(t, IsCooldownStatusCode(500))
	require.True(t, IsCooldownStatusCode(502))
	require.False(t, IsCooldownStatusCode(400))
	require.False(t, IsCooldownStatusCode(401))
}
