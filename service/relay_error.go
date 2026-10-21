package service

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// sameChannelRetryCountKey counts how many same-channel retries the current
// attempt has already used. The relay loop increments it after acting on a
// "retry_same_channel" decision and resets it when it switches channel.
const sameChannelRetryCountKey = "same_channel_retry_count"

// SameChannelRetryCount returns how many same-channel retries this request has
// spent on the channel it is currently attempting.
func SameChannelRetryCount(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return c.GetInt(sameChannelRetryCountKey)
}

// MarkSameChannelRetry records one same-channel retry.
func MarkSameChannelRetry(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(sameChannelRetryCountKey, SameChannelRetryCount(c)+1)
}

// ResetSameChannelRetry clears the counter, which a channel switch must do so
// the next channel gets its own same-channel retry budget.
func ResetSameChannelRetry(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(sameChannelRetryCountKey, 0)
}

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	// 客户端已断开：用已取消的 context 重新选号只会再失败一次，还会被误报成渠道耗尽。
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return PolicyDecision{Action: "stop", Reason: "client_gone", Source: "system"}
	}
	// 响应已提交：换渠道会往同一个 HTTP 响应里写第二段内容。只写了 SSE 心跳
	// （客户端忽略的注释行）不算提交，仍允许换号。
	if operation_setting.GetRetryFailoverSetting().StopAfterResponseWritten && common.ResponseBodyWritten(c) {
		return PolicyDecision{Action: "stop", Reason: "response_committed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if !operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
	}
	// 瞬时错误先在同一渠道原地重试：上游抖动换渠道并不比原地重试更快，还会
	// 浪费一个候选渠道。凭证/配置类错误（channel: 前缀）换渠道才有意义。
	retryFailover := operation_setting.GetRetryFailoverSetting()
	if retryFailover.SameChannelRetries > 0 &&
		SameChannelRetryCount(c) < retryFailover.SameChannelRetries &&
		operation_setting.IsSameChannelRetryStatusCode(code) {
		return PolicyDecision{Action: "retry_same_channel", Reason: "same_channel_retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	switch DecideRelayRetry(c, openaiErr, retryTimes).Action {
	case "retry", "retry_same_channel":
		return true
	}
	return false
}

// SleepWithContext waits for d and reports false when the request context ended
// first, so a backoff never outlives the client that asked for it.
func SleepWithContext(ctx context.Context, d time.Duration) bool {
	if ctx == nil {
		if d > 0 {
			time.Sleep(d)
		}
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// markChannelCooldownOnError temporarily removes a channel from selection when
// the cooldown setting is on and the error status is in its range. It reports
// whether a cooldown was recorded, in which case the caller must not also
// disable the channel permanently.
func markChannelCooldownOnError(c *gin.Context, channelID int, err *types.NewAPIError) bool {
	retryFailover := operation_setting.GetRetryFailoverSetting()
	if retryFailover.CooldownSeconds <= 0 || channelID <= 0 {
		return false
	}
	// 客户端断开或本地拒绝产生的错误与渠道健康无关，不该冷却渠道。
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return false
	}
	if types.IsSkipRetryError(err) || !operation_setting.IsCooldownStatusCode(err.StatusCode) {
		return false
	}
	modelName := ""
	if c != nil {
		modelName = c.GetString("original_model")
	}
	model.MarkChannelCooldown(
		channelID,
		modelName,
		time.Duration(retryFailover.CooldownSeconds)*time.Second,
		err.StatusCode,
		common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode()),
	)
	return true
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode())))

	// 冷却优先于永久禁用：落在冷却状态码内的错误只把渠道临时移出候选池，
	// 到期自动恢复，不再一次抖动就永久摘除渠道。冷却范围之外的错误维持原行为。
	cooledDown := markChannelCooldownOnError(c, channelError.ChannelId, err)

	if !cooledDown && ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode()
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		other := model.NewLogOther()
		if c.Request != nil && c.Request.URL != nil {
			other.SetPublic("request_path", c.Request.URL.Path)
		}
		other.SetPublic("error_type", err.GetErrorType())
		other.SetPublic("error_code", err.GetErrorCode())
		other.SetPublic("status_code", err.StatusCode)
		AppendRelayLogAdminInfo(c, relayInfo, other)
		AppendResponseModelLogInfo(relayInfo, other)
		AppendTaskPluginContextAuditInfo(c, other)
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelError.ChannelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}
