package operation_setting

import (
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/setting/config"
)

// RetryFailoverSetting controls how a relay attempt fails over to another
// channel. The defaults keep the pre-existing behavior: only the two
// corrections that cannot change a successful request (excluding channels that
// already failed in this request, and refusing to retry once bytes reached the
// client) are enabled.
type RetryFailoverSetting struct {
	// ExcludeFailedChannels keeps a request from re-selecting a channel that
	// already failed for it. Without this, a group with a single priority tier
	// can re-draw the exact channel that just failed.
	ExcludeFailedChannels bool `json:"exclude_failed_channels"`
	// StopAfterResponseWritten refuses to retry once bytes have been written to
	// the client, because a second attempt would append another stream to the
	// same HTTP response.
	StopAfterResponseWritten bool `json:"stop_after_response_written"`
	// SameChannelRetries is how many times a transient error is retried on the
	// same channel before switching. Zero disables same-channel retries.
	SameChannelRetries int `json:"same_channel_retries"`
	// SameChannelStatusCodes limits which status codes qualify for a
	// same-channel retry.
	SameChannelStatusCodes string `json:"same_channel_status_codes"`
	// BackoffMs is the first same-channel retry delay; it doubles per retry.
	BackoffMs int `json:"backoff_ms"`
	// BackoffMaxMs caps the same-channel retry delay.
	BackoffMaxMs int `json:"backoff_max_ms"`
	// SelectionExhaustedBackoffMs is how long to wait before clearing the
	// exclusion set and re-selecting after an upstream 503 emptied the pool.
	SelectionExhaustedBackoffMs int `json:"selection_exhausted_backoff_ms"`
	// CooldownSeconds temporarily removes a failing channel from selection
	// instead of disabling it permanently. Zero disables cooldowns.
	CooldownSeconds int `json:"cooldown_seconds"`
	// CooldownStatusCodes limits which status codes start a cooldown.
	CooldownStatusCodes string `json:"cooldown_status_codes"`
}

var retryFailoverSetting = RetryFailoverSetting{
	ExcludeFailedChannels:       true,
	StopAfterResponseWritten:    true,
	SameChannelRetries:          0,
	SameChannelStatusCodes:      "429,500,502,503,529",
	BackoffMs:                   500,
	BackoffMaxMs:                8000,
	SelectionExhaustedBackoffMs: 2000,
	CooldownSeconds:             0,
	CooldownStatusCodes:         "429,500-503,529",
}

func init() {
	config.GlobalConfig.Register("retry_failover_setting", &retryFailoverSetting)
}

// retryFailoverRanges caches the parsed status code ranges so the relay path
// does not re-parse the option strings on every attempt.
type retryFailoverRanges struct {
	sameChannelRaw    string
	sameChannelRanges []StatusCodeRange
	cooldownRaw       string
	cooldownRanges    []StatusCodeRange
}

var retryFailoverRangeCache atomic.Pointer[retryFailoverRanges]

func GetRetryFailoverSetting() RetryFailoverSetting {
	setting := retryFailoverSetting
	if setting.SameChannelRetries < 0 {
		setting.SameChannelRetries = 0
	}
	if setting.SameChannelRetries > 99 {
		setting.SameChannelRetries = 99
	}
	if setting.BackoffMs < 0 {
		setting.BackoffMs = 0
	}
	if setting.BackoffMaxMs < 0 {
		setting.BackoffMaxMs = 0
	}
	if setting.BackoffMaxMs > 0 && setting.BackoffMs > setting.BackoffMaxMs {
		setting.BackoffMs = setting.BackoffMaxMs
	}
	if setting.SelectionExhaustedBackoffMs < 0 {
		setting.SelectionExhaustedBackoffMs = 0
	}
	if setting.CooldownSeconds < 0 {
		setting.CooldownSeconds = 0
	}
	return setting
}

func retryFailoverRangesSnapshot() *retryFailoverRanges {
	setting := GetRetryFailoverSetting()
	if cached := retryFailoverRangeCache.Load(); cached != nil &&
		cached.sameChannelRaw == setting.SameChannelStatusCodes &&
		cached.cooldownRaw == setting.CooldownStatusCodes {
		return cached
	}
	snapshot := &retryFailoverRanges{
		sameChannelRaw: setting.SameChannelStatusCodes,
		cooldownRaw:    setting.CooldownStatusCodes,
	}
	snapshot.sameChannelRanges, _ = ParseHTTPStatusCodeRanges(setting.SameChannelStatusCodes)
	snapshot.cooldownRanges, _ = ParseHTTPStatusCodeRanges(setting.CooldownStatusCodes)
	retryFailoverRangeCache.Store(snapshot)
	return snapshot
}

// IsSameChannelRetryStatusCode reports whether a status code may be retried on
// the channel that produced it.
func IsSameChannelRetryStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(retryFailoverRangesSnapshot().sameChannelRanges, code)
}

// IsCooldownStatusCode reports whether a status code starts a channel cooldown.
func IsCooldownStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(retryFailoverRangesSnapshot().cooldownRanges, code)
}

// SameChannelRetryBackoff returns the delay before the n-th (1-based)
// same-channel retry: backoff_ms doubled n-1 times, capped at backoff_max_ms.
func SameChannelRetryBackoff(retryCount int) time.Duration {
	setting := GetRetryFailoverSetting()
	if retryCount <= 1 || setting.BackoffMs <= 0 {
		return time.Duration(setting.BackoffMs) * time.Millisecond
	}
	delay := setting.BackoffMs
	maxDelay := setting.BackoffMaxMs
	if maxDelay <= 0 {
		maxDelay = delay
	}
	for i := 1; i < retryCount; i++ {
		if delay >= maxDelay/2 {
			return time.Duration(maxDelay) * time.Millisecond
		}
		delay *= 2
	}
	return time.Duration(delay) * time.Millisecond
}
