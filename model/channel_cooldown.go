package model

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ChannelCooldownKeyPrefix namespaces the Redis mirror of channel cooldowns.
const ChannelCooldownKeyPrefix = "channel_cooldown:"

// cooldownRedisRefreshInterval bounds how often one instance re-reads the
// Redis mirror for the same channel. Without it every candidate lookup in
// every request would cost a Redis round-trip.
const cooldownRedisRefreshInterval = time.Second

// ChannelCooldownEntry is the temporary removal of one channel from selection.
// Deadlines are Unix milliseconds so a cooldown shorter than a second still
// takes effect; second granularity would round a sub-second window into the
// current second and expire it immediately.
type ChannelCooldownEntry struct {
	UntilUnixMs int64            `json:"until_unix_ms"`
	StatusCode  int              `json:"status_code"`
	Reason      string           `json:"reason"`
	Models      map[string]int64 `json:"models,omitempty"`
}

func (e *ChannelCooldownEntry) covers(modelName string, now time.Time) bool {
	if e == nil {
		return false
	}
	nowMs := now.UnixMilli()
	if e.UntilUnixMs > nowMs {
		return true
	}
	return e.Models[modelName] > nowMs
}

func (e *ChannelCooldownEntry) merge(other *ChannelCooldownEntry) {
	if other == nil {
		return
	}
	// Only extend: a shorter cooldown never truncates a longer active one.
	if other.UntilUnixMs > e.UntilUnixMs {
		e.UntilUnixMs = other.UntilUnixMs
		e.StatusCode = other.StatusCode
		e.Reason = other.Reason
	}
	for model, until := range other.Models {
		if until <= e.Models[model] {
			continue
		}
		if e.Models == nil {
			e.Models = make(map[string]int64)
		}
		e.Models[model] = until
	}
}

func (e *ChannelCooldownEntry) maxUntilMs() int64 {
	if e == nil {
		return 0
	}
	until := e.UntilUnixMs
	for _, modelUntil := range e.Models {
		if modelUntil > until {
			until = modelUntil
		}
	}
	return until
}

type channelCooldownRecord struct {
	entry     *ChannelCooldownEntry
	checkedAt time.Time
}

var channelCooldownCache = struct {
	mu      sync.RWMutex
	records map[int]*channelCooldownRecord
}{records: make(map[int]*channelCooldownRecord)}

// MarkChannelCooldown temporarily removes a channel from selection. modelName
// scopes the cooldown to one model; an empty modelName cools the whole channel.
// Repeated marks only extend the deadline.
func MarkChannelCooldown(channelID int, modelName string, d time.Duration, statusCode int, reason string) {
	if channelID <= 0 || d <= 0 {
		return
	}
	until := time.Now().Add(d).UnixMilli()
	incoming := &ChannelCooldownEntry{StatusCode: statusCode, Reason: reason}
	if modelName == "" {
		incoming.UntilUnixMs = until
	} else {
		incoming.Models = map[string]int64{modelName: until}
	}

	channelCooldownCache.mu.Lock()
	record := channelCooldownCache.records[channelID]
	if record == nil {
		record = &channelCooldownRecord{}
		channelCooldownCache.records[channelID] = record
	}
	if record.entry == nil {
		record.entry = &ChannelCooldownEntry{}
	}
	record.entry.merge(incoming)
	record.checkedAt = time.Now()
	// Snapshot before releasing the lock: the mirror write must not read a map
	// another goroutine can still mutate.
	merged := &ChannelCooldownEntry{
		UntilUnixMs: record.entry.UntilUnixMs,
		StatusCode:  record.entry.StatusCode,
		Reason:      record.entry.Reason,
	}
	if len(record.entry.Models) > 0 {
		merged.Models = maps.Clone(record.entry.Models)
	}
	channelCooldownCache.mu.Unlock()

	writeChannelCooldownToRedis(channelID, merged)
}

// ChannelInCooldown reports whether a channel is currently cooled down for the
// given model.
func ChannelInCooldown(channelID int, modelName string) bool {
	now := time.Now()
	channelCooldownCache.mu.RLock()
	record := channelCooldownCache.records[channelID]
	inCooldown := record != nil && record.entry.covers(modelName, now)
	stale := record == nil || now.Sub(record.checkedAt) >= cooldownRedisRefreshInterval
	channelCooldownCache.mu.RUnlock()

	if inCooldown || !stale || !common.RedisEnabled {
		return inCooldown
	}
	return refreshChannelCooldownFromRedis(channelID, modelName, now)
}

// ClearChannelCooldown drops a channel's cooldown, locally and in the mirror.
func ClearChannelCooldown(channelID int) {
	channelCooldownCache.mu.Lock()
	delete(channelCooldownCache.records, channelID)
	channelCooldownCache.mu.Unlock()
	if common.RedisEnabled {
		_ = common.RedisDel(fmt.Sprintf("%s%d", ChannelCooldownKeyPrefix, channelID))
	}
}

// refreshChannelCooldownFromRedis re-reads the mirror so an instance learns
// about cooldowns started by another instance. It reports whether the channel
// is cooled down for modelName.
func refreshChannelCooldownFromRedis(channelID int, modelName string, now time.Time) bool {
	remote := &ChannelCooldownEntry{}
	if raw, err := common.RedisGet(fmt.Sprintf("%s%d", ChannelCooldownKeyPrefix, channelID)); err == nil && raw != "" {
		if err := common.UnmarshalJsonStr(raw, remote); err != nil {
			remote = &ChannelCooldownEntry{}
		}
	}

	channelCooldownCache.mu.Lock()
	record := channelCooldownCache.records[channelID]
	if record == nil {
		record = &channelCooldownRecord{}
		channelCooldownCache.records[channelID] = record
	}
	// The local entry may be newer than the mirror (another instance has not
	// seen this instance's write yet), so merge instead of replacing.
	merged := &ChannelCooldownEntry{}
	merged.merge(record.entry)
	merged.merge(remote)
	if merged.maxUntilMs() > now.UnixMilli() {
		record.entry = merged
	} else {
		record.entry = nil
	}
	// 负结果也要记住 checkedAt，否则每个候选渠道每次选号都会打一次 Redis。
	record.checkedAt = now
	inCooldown := record.entry.covers(modelName, now)
	channelCooldownCache.mu.Unlock()
	return inCooldown
}

// excludeCooledDownChannels drops channels currently inside a cooldown window.
// It is a no-op while the cooldown setting is off, so the default path pays
// nothing.
func excludeCooledDownChannels(ids []int, modelName string) []int {
	if len(ids) == 0 || operation_setting.GetRetryFailoverSetting().CooldownSeconds <= 0 {
		return ids
	}
	kept := make([]int, 0, len(ids))
	for _, id := range ids {
		if ChannelInCooldown(id, modelName) {
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

func writeChannelCooldownToRedis(channelID int, entry *ChannelCooldownEntry) {
	if !common.RedisEnabled {
		return
	}
	ttl := time.Until(time.UnixMilli(entry.maxUntilMs()))
	if ttl <= 0 {
		return
	}
	key := fmt.Sprintf("%s%d", ChannelCooldownKeyPrefix, channelID)
	// 读改写以保持「只延长不缩短」：另一个实例可能刚写入更长的冷却。
	if raw, err := common.RedisGet(key); err == nil && raw != "" {
		remote := &ChannelCooldownEntry{}
		if err := common.UnmarshalJsonStr(raw, remote); err == nil {
			merged := &ChannelCooldownEntry{}
			merged.merge(entry)
			merged.merge(remote)
			entry = merged
			if mergedTTL := time.Until(time.UnixMilli(entry.maxUntilMs())); mergedTTL > ttl {
				ttl = mergedTTL
			}
		}
	}
	payload, err := common.Marshal(entry)
	if err != nil {
		return
	}
	if err := common.RedisSet(key, string(payload), ttl); err != nil {
		common.SysLog(fmt.Sprintf("failed to write channel cooldown: channel_id=%d, error=%v", channelID, err))
	}
}
