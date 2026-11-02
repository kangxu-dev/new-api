package service

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

const adaptiveChannelBucketCount = 60

// AdaptiveChannelProbeBucket is the persistable form of one probe minute. The
// probe task stores these in its system-task result so a new runner (or another
// master node) can rebuild the in-memory baseline without new tables.
type AdaptiveChannelProbeBucket struct {
	Minute          int64 `json:"minute"`
	Requests        int64 `json:"requests"`
	Successes       int64 `json:"successes"`
	ServerErrors    int64 `json:"server_errors"`
	FirstByteTotal  int64 `json:"first_byte_total"`
	FirstByteSample int64 `json:"first_byte_sample"`
}

// AdaptiveChannelProbeWindow is one channel's persisted probe history for a
// probe model.
type AdaptiveChannelProbeWindow struct {
	ProbeModel string                       `json:"probe_model"`
	ChannelID  int                          `json:"channel_id"`
	Buckets    []AdaptiveChannelProbeBucket `json:"buckets"`
}

type adaptiveChannelKey struct {
	group     string
	model     string
	channelID int
}

type adaptiveChannelProbeKey struct {
	model     string
	channelID int
}

type adaptiveChannelBucket struct {
	minute          int64
	requests        int64
	successes       int64
	serverErrors    int64
	firstByteTotal  int64
	firstByteSample int64
}

type adaptiveChannelWindow struct {
	buckets   [adaptiveChannelBucketCount]adaptiveChannelBucket
	lastTouch int64
}

var adaptiveChannelWindows = struct {
	sync.RWMutex
	windows     map[adaptiveChannelKey]*adaptiveChannelWindow
	lastPruneAt int64
}{windows: make(map[adaptiveChannelKey]*adaptiveChannelWindow)}

// adaptiveChannelProbeWindows is deliberately separate from the live windows:
// probe observations are keyed by probe model and channel, never by a business
// group/model, so they cannot be mistaken for measured business traffic.
var adaptiveChannelProbeWindows = struct {
	sync.RWMutex
	windows     map[adaptiveChannelProbeKey]*adaptiveChannelWindow
	lastPruneAt int64
}{windows: make(map[adaptiveChannelProbeKey]*adaptiveChannelWindow)}

// RecordAdaptiveChannelOutcome records upstream outcomes used by the optional
// routing score. Client errors and local rejections are intentionally omitted.
func RecordAdaptiveChannelOutcome(group, model string, channelID, statusCode int, success bool, firstByteMs int64, hasFirstByte bool) {
	if channelID <= 0 || group == "" || model == "" || !success && (statusCode < 500 || statusCode > 599) {
		return
	}
	minute := time.Now().Unix() / 60
	key := adaptiveChannelKey{group: group, model: model, channelID: channelID}

	adaptiveChannelWindows.Lock()
	defer adaptiveChannelWindows.Unlock()
	window := adaptiveChannelWindows.windows[key]
	if window == nil {
		window = &adaptiveChannelWindow{}
		adaptiveChannelWindows.windows[key] = window
	}
	recordAdaptiveBucket(window, minute, success, firstByteMs, hasFirstByte)

	if minute-adaptiveChannelWindows.lastPruneAt >= adaptiveChannelBucketCount {
		cutoff := minute - adaptiveChannelBucketCount
		for staleKey, staleWindow := range adaptiveChannelWindows.windows {
			if staleWindow.lastTouch < cutoff {
				delete(adaptiveChannelWindows.windows, staleKey)
			}
		}
		adaptiveChannelWindows.lastPruneAt = minute
	}
}

func recordAdaptiveBucket(window *adaptiveChannelWindow, minute int64, success bool, firstByteMs int64, hasFirstByte bool) {
	bucket := &window.buckets[minute%adaptiveChannelBucketCount]
	if bucket.minute != minute {
		*bucket = adaptiveChannelBucket{minute: minute}
	}
	bucket.requests++
	if success {
		bucket.successes++
	} else {
		bucket.serverErrors++
	}
	if hasFirstByte && firstByteMs >= 0 {
		bucket.firstByteTotal += firstByteMs
		bucket.firstByteSample++
	}
	window.lastTouch = minute
}

// RecordAdaptiveChannelProbeOutcome records one real probe result for the
// channel baseline. Client errors, rate limits, transport failures and local
// failures never reach the health totals; only a successful response or an
// upstream 5xx does.
func RecordAdaptiveChannelProbeOutcome(probeModel string, channelID, statusCode int, success bool, firstByteMs int64, hasFirstByte bool) {
	if channelID <= 0 || probeModel == "" || !success && (statusCode < 500 || statusCode > 599) {
		return
	}
	minute := time.Now().Unix() / 60
	key := adaptiveChannelProbeKey{model: probeModel, channelID: channelID}

	adaptiveChannelProbeWindows.Lock()
	defer adaptiveChannelProbeWindows.Unlock()
	window := adaptiveChannelProbeWindows.windows[key]
	if window == nil {
		window = &adaptiveChannelWindow{}
		adaptiveChannelProbeWindows.windows[key] = window
	}
	recordAdaptiveBucket(window, minute, success, firstByteMs, hasFirstByte)

	if minute-adaptiveChannelProbeWindows.lastPruneAt >= adaptiveChannelBucketCount {
		cutoff := minute - adaptiveChannelBucketCount
		for staleKey, staleWindow := range adaptiveChannelProbeWindows.windows {
			if staleWindow.lastTouch < cutoff {
				delete(adaptiveChannelProbeWindows.windows, staleKey)
			}
		}
		adaptiveChannelProbeWindows.lastPruneAt = minute
	}
}

// adaptiveChannelAggregate is one channel's window totals for a group and model.
type adaptiveChannelAggregate struct {
	channelID        int
	requests         int64
	successes        int64
	serverErrors     int64
	firstByteTotal   int64
	firstByteSamples int64
}

func (a adaptiveChannelAggregate) avgFirstByteMs() float64 {
	if a.firstByteSamples <= 0 {
		return 0
	}
	return float64(a.firstByteTotal) / float64(a.firstByteSamples)
}

// aggregate sums the buckets that fall inside [cutoff, minute].
func (w *adaptiveChannelWindow) aggregate(channelID int, cutoff, minute int64) adaptiveChannelAggregate {
	agg := adaptiveChannelAggregate{channelID: channelID}
	for _, bucket := range w.buckets {
		if bucket.minute < cutoff || bucket.minute > minute {
			continue
		}
		agg.requests += bucket.requests
		agg.successes += bucket.successes
		agg.serverErrors += bucket.serverErrors
		agg.firstByteTotal += bucket.firstByteTotal
		agg.firstByteSamples += bucket.firstByteSample
	}
	return agg
}

// adaptiveChannelWeightMultipliers turns window totals into weight multipliers,
// dispatching on the configured latency mode.
func adaptiveChannelWeightMultipliers(setting operation_setting.AdaptiveChannelRoutingSetting, aggregates []adaptiveChannelAggregate) map[int]float64 {
	if len(aggregates) == 0 {
		return nil
	}
	if setting.LatencyMode == operation_setting.AdaptiveLatencyModeRatio {
		return adaptiveChannelRatioMultipliers(setting, aggregates)
	}

	result := make(map[int]float64, len(aggregates))
	for _, agg := range aggregates {
		score := adaptiveChannelScore(agg.requests, agg.successes, agg.serverErrors, agg.firstByteTotal, agg.firstByteSamples)
		result[agg.channelID] = 0.5 + score/100
	}
	return result
}

// adaptiveChannelMultiplier is one channel's effective routing multiplier and
// where it came from. The source is exposed so the snapshot and UI can tell a
// measured business score apart from a probe-built baseline.
type adaptiveChannelMultiplier struct {
	multiplier float64
	source     string
}

const (
	adaptiveChannelScoreSourceLive  = "live"
	adaptiveChannelScoreSourceProbe = "probe"
)

func adaptiveChannelGroupAggregates(group, model string, cutoff, minute int64) []adaptiveChannelAggregate {
	aggregates := make([]adaptiveChannelAggregate, 0, 4)
	adaptiveChannelWindows.RLock()
	for key, window := range adaptiveChannelWindows.windows {
		if key.group != group || key.model != model {
			continue
		}
		aggregates = append(aggregates, window.aggregate(key.channelID, cutoff, minute))
	}
	adaptiveChannelWindows.RUnlock()
	return aggregates
}

func adaptiveChannelProbeAggregates(probeModel string, cutoff, minute int64) []adaptiveChannelAggregate {
	aggregates := make([]adaptiveChannelAggregate, 0, 4)
	adaptiveChannelProbeWindows.RLock()
	for key, window := range adaptiveChannelProbeWindows.windows {
		if key.model != probeModel {
			continue
		}
		agg := window.aggregate(key.channelID, cutoff, minute)
		if agg.requests == 0 {
			continue
		}
		aggregates = append(aggregates, agg)
	}
	adaptiveChannelProbeWindows.RUnlock()
	return aggregates
}

func adaptiveChannelProbeMultipliers(setting operation_setting.AdaptiveChannelRoutingSetting, probeModel string) map[int]float64 {
	if !setting.ProbeEnabled || probeModel == "" {
		return nil
	}
	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)
	reached := make([]adaptiveChannelAggregate, 0, 4)
	for _, agg := range adaptiveChannelProbeAggregates(probeModel, cutoff, minute) {
		if agg.requests >= int64(setting.MinSamples) {
			reached = append(reached, agg)
		}
	}
	return adaptiveChannelWeightMultipliers(setting, reached)
}

// adaptiveChannelAssignments merges the measured business score with the probe
// baseline. A channel that reached MinSamples always uses its live result, even
// when ratio mode returns nil because every sampled channel failed; only
// channels below the threshold fall back to the baseline.
func adaptiveChannelAssignments(setting operation_setting.AdaptiveChannelRoutingSetting, aggregates []adaptiveChannelAggregate, probeMultipliers map[int]float64) map[int]adaptiveChannelMultiplier {
	reached := make(map[int]struct{}, len(aggregates))
	liveAggregates := make([]adaptiveChannelAggregate, 0, len(aggregates))
	for _, agg := range aggregates {
		if agg.requests < int64(setting.MinSamples) {
			continue
		}
		reached[agg.channelID] = struct{}{}
		liveAggregates = append(liveAggregates, agg)
	}

	result := make(map[int]adaptiveChannelMultiplier, len(liveAggregates)+len(probeMultipliers))
	for id, multiplier := range adaptiveChannelWeightMultipliers(setting, liveAggregates) {
		result[id] = adaptiveChannelMultiplier{multiplier: multiplier, source: adaptiveChannelScoreSourceLive}
	}
	for id, multiplier := range probeMultipliers {
		if _, ok := reached[id]; ok {
			continue
		}
		result[id] = adaptiveChannelMultiplier{multiplier: multiplier, source: adaptiveChannelScoreSourceProbe}
	}
	return result
}

// AdaptiveChannelWeightMultipliers returns neutral-to-bounded multipliers for
// channels with enough recent observations, falling back to the probe baseline
// for channels that have not reached MinSamples yet. Static priority tiers stay
// intact.
func AdaptiveChannelWeightMultipliers(group, model string) map[int]float64 {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || group == "" || model == "" {
		return nil
	}

	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)
	aggregates := adaptiveChannelGroupAggregates(group, model, cutoff, minute)
	assignments := adaptiveChannelAssignments(setting, aggregates, adaptiveChannelProbeMultipliers(setting, setting.ProbeModel))
	if len(assignments) == 0 {
		return nil
	}

	result := make(map[int]float64, len(assignments))
	for id, assignment := range assignments {
		result[id] = assignment.multiplier
	}
	return result
}

// AdaptiveChannelProbeRequestCount reports how many probe observations the
// channel has for the current probe model inside the scoring window. The probe
// task uses it to stop once MinSamples is reached and to rotate fairly.
func AdaptiveChannelProbeRequestCount(probeModel string, channelID int) int64 {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || setting.ProbeModel != probeModel {
		return 0
	}
	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)

	adaptiveChannelProbeWindows.RLock()
	defer adaptiveChannelProbeWindows.RUnlock()
	window := adaptiveChannelProbeWindows.windows[adaptiveChannelProbeKey{model: probeModel, channelID: channelID}]
	if window == nil {
		return 0
	}
	return window.aggregate(channelID, cutoff, minute).requests
}

// AdaptiveChannelNeedsProbeBaseline reports whether a channel still needs probe
// samples. A channel with no recent live traffic needs a bootstrap baseline; a
// channel whose every active business group/model already reached MinSamples
// does not, so the probe task stops spending upstream usage on it.
func AdaptiveChannelNeedsProbeBaseline(channelID int) bool {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || channelID <= 0 {
		return false
	}
	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)

	hasActive := false
	adaptiveChannelWindows.RLock()
	defer adaptiveChannelWindows.RUnlock()
	for key, window := range adaptiveChannelWindows.windows {
		if key.channelID != channelID {
			continue
		}
		agg := window.aggregate(channelID, cutoff, minute)
		if agg.requests == 0 {
			continue
		}
		hasActive = true
		if agg.requests < int64(setting.MinSamples) {
			return true
		}
	}
	return !hasActive
}

// ExportAdaptiveChannelProbeState returns the persisted form of the current
// probe model's history. Only the 60-minute bucket ring is exported; the task
// result can be replayed on another node without losing minute boundaries.
func ExportAdaptiveChannelProbeState(probeModel string) []AdaptiveChannelProbeWindow {
	if probeModel == "" {
		return nil
	}
	minute := time.Now().Unix()/60 - adaptiveChannelBucketCount
	adaptiveChannelProbeWindows.RLock()
	defer adaptiveChannelProbeWindows.RUnlock()
	result := make([]AdaptiveChannelProbeWindow, 0, len(adaptiveChannelProbeWindows.windows))
	for key, window := range adaptiveChannelProbeWindows.windows {
		if key.model != probeModel {
			continue
		}
		buckets := make([]AdaptiveChannelProbeBucket, 0, adaptiveChannelBucketCount)
		for _, bucket := range window.buckets {
			if bucket.minute <= minute || bucket.requests == 0 {
				continue
			}
			buckets = append(buckets, AdaptiveChannelProbeBucket{
				Minute:          bucket.minute,
				Requests:        bucket.requests,
				Successes:       bucket.successes,
				ServerErrors:    bucket.serverErrors,
				FirstByteTotal:  bucket.firstByteTotal,
				FirstByteSample: bucket.firstByteSample,
			})
		}
		if len(buckets) == 0 {
			continue
		}
		result = append(result, AdaptiveChannelProbeWindow{ProbeModel: probeModel, ChannelID: key.channelID, Buckets: buckets})
	}
	slices.SortFunc(result, func(a, b AdaptiveChannelProbeWindow) int {
		return a.ChannelID - b.ChannelID
	})
	return result
}

// MergeAdaptiveChannelProbeState merges persisted buckets into the in-memory
// ring. Counts are merged by minute and never subtracted: replaying an older
// task result is idempotent, and a local bucket from a newer minute is kept.
func MergeAdaptiveChannelProbeState(windows []AdaptiveChannelProbeWindow) {
	adaptiveChannelProbeWindows.Lock()
	defer adaptiveChannelProbeWindows.Unlock()
	for _, persisted := range windows {
		if persisted.ProbeModel == "" || persisted.ChannelID <= 0 {
			continue
		}
		key := adaptiveChannelProbeKey{model: persisted.ProbeModel, channelID: persisted.ChannelID}
		window := adaptiveChannelProbeWindows.windows[key]
		if window == nil {
			window = &adaptiveChannelWindow{}
			adaptiveChannelProbeWindows.windows[key] = window
		}
		for _, persistedBucket := range persisted.Buckets {
			if persistedBucket.Minute <= 0 || persistedBucket.Requests <= 0 {
				continue
			}
			bucket := &window.buckets[persistedBucket.Minute%adaptiveChannelBucketCount]
			switch {
			case bucket.minute < persistedBucket.Minute:
				*bucket = adaptiveChannelBucket{
					minute:          persistedBucket.Minute,
					requests:        persistedBucket.Requests,
					successes:       persistedBucket.Successes,
					serverErrors:    persistedBucket.ServerErrors,
					firstByteTotal:  persistedBucket.FirstByteTotal,
					firstByteSample: persistedBucket.FirstByteSample,
				}
			case bucket.minute == persistedBucket.Minute:
				bucket.requests = max(bucket.requests, persistedBucket.Requests)
				bucket.successes = max(bucket.successes, persistedBucket.Successes)
				bucket.serverErrors = max(bucket.serverErrors, persistedBucket.ServerErrors)
				bucket.firstByteTotal = max(bucket.firstByteTotal, persistedBucket.FirstByteTotal)
				bucket.firstByteSample = max(bucket.firstByteSample, persistedBucket.FirstByteSample)
			}
			window.lastTouch = max(window.lastTouch, persistedBucket.Minute)
		}
	}
}

// adaptiveChannelRatioMultipliers scores each channel against the fastest one
// observed for the same group and model. Scoring relative to the best removes
// the need for an absolute reference latency: the legacy curve was tuned for
// sub-second first bytes and flattens out long before the multi-second range,
// so a large latency gap barely moved the multiplier on slow upstreams.
func adaptiveChannelRatioMultipliers(setting operation_setting.AdaptiveChannelRoutingSetting, aggregates []adaptiveChannelAggregate) map[int]float64 {
	bestMs := 0.0
	for _, agg := range aggregates {
		if avg := agg.avgFirstByteMs(); avg > 0 && (bestMs == 0 || avg < bestMs) {
			bestMs = avg
		}
	}

	weights := make(map[int]float64, len(aggregates))
	maxWeight := 0.0
	for _, agg := range aggregates {
		// Errors dominate when present: a channel failing a quarter of its
		// requests must not be rescued by being the fastest.
		health := 1.0
		if agg.requests > 0 {
			health = 1 - float64(agg.serverErrors)/float64(agg.requests)
		}
		latencyFactor := 1.0
		if bestMs > 0 {
			if avg := agg.avgFirstByteMs(); avg > 0 {
				latencyFactor = math.Pow(bestMs/avg, setting.LatencyExponent)
			}
		}
		weight := health * latencyFactor
		weights[agg.channelID] = weight
		if weight > maxWeight {
			maxWeight = weight
		}
	}
	if maxWeight <= 0 {
		// Every sampled channel is fully failing; the score has nothing to
		// prefer, so stay out of the way and let the plain weighting apply.
		return nil
	}

	result := make(map[int]float64, len(weights))
	for id, weight := range weights {
		multiplier := setting.MultiplierMax * (weight / maxWeight)
		if multiplier < setting.MultiplierMin {
			multiplier = setting.MultiplierMin
		}
		if multiplier > setting.MultiplierMax {
			multiplier = setting.MultiplierMax
		}
		result[id] = multiplier
	}
	return result
}

func adaptiveChannelScore(requests, successes, serverErrors, firstByteTotal, firstByteSamples int64) float64 {
	if requests <= 0 {
		return 50
	}
	successRate := float64(successes) / float64(requests) * 100
	serverErrorRate := float64(serverErrors) / float64(requests) * 100
	score := successRate*0.625 + (100-serverErrorRate)*0.375
	if firstByteSamples > 0 {
		avgFirstByteMs := float64(firstByteTotal) / float64(firstByteSamples)
		firstByteScore := 100 / (1 + avgFirstByteMs/2000)
		score = score*0.8 + firstByteScore*0.2
	}
	return max(0, min(100, score))
}

// AdaptiveChannelScoreSnapshot is one channel's current routing input, exactly
// as the selector sees it.
type AdaptiveChannelScoreSnapshot struct {
	ChannelID int `json:"channel_id"`
	// ChannelName is filled by the API layer, which has the channel cache.
	ChannelName    string  `json:"channel_name"`
	Requests       int64   `json:"requests"`
	Successes      int64   `json:"successes"`
	ServerErrors   int64   `json:"server_errors"`
	AvgFirstByteMs float64 `json:"avg_first_byte_ms"`
	// Scored reports whether this channel carries a multiplier. A channel that
	// reached MinSamples uses its live result; a channel below the threshold may
	// carry the probe-built baseline instead.
	Scored     bool    `json:"scored"`
	Multiplier float64 `json:"multiplier"`
	// EffectiveSource is "live" or "probe" when Scored is true, and empty when
	// the channel is neutral. It describes the multiplier only; Requests,
	// Successes and AvgFirstByteMs always describe live business traffic.
	EffectiveSource string `json:"effective_source"`
	// ProbeModel is set when the effective multiplier came from the baseline.
	ProbeModel string `json:"probe_model,omitempty"`
}

// AdaptiveChannelProbeSnapshot is one channel's probe-built baseline. It is
// reported separately from business groups so a channel can show a ready
// baseline even when the business model has never been requested.
type AdaptiveChannelProbeSnapshot struct {
	ChannelID int `json:"channel_id"`
	// ChannelName is filled by the API layer, which has the channel cache.
	ChannelName    string  `json:"channel_name"`
	ProbeModel     string  `json:"probe_model"`
	Requests       int64   `json:"requests"`
	Successes      int64   `json:"successes"`
	ServerErrors   int64   `json:"server_errors"`
	AvgFirstByteMs float64 `json:"avg_first_byte_ms"`
	Scored         bool    `json:"scored"`
	Multiplier     float64 `json:"multiplier"`
	Source         string  `json:"source"`
}

// AdaptiveChannelScoreGroup is the score state for one group and model.
type AdaptiveChannelScoreGroup struct {
	Group    string                         `json:"group"`
	Model    string                         `json:"model"`
	Channels []AdaptiveChannelScoreSnapshot `json:"channels"`
}

// SnapshotAdaptiveChannelScores reports what the score currently holds for every
// group and model it has seen. The multiplier is otherwise invisible, which
// makes "the score is not applying" and "the score is applying but the channels
// are equally good" indistinguishable from the outside.
//
// The optional candidate set tells the snapshot which channels belong to a
// group/model. That lets an already-seen business model report a channel that
// has no live window yet but already has a probe baseline, matching what the
// selector would actually use.
func SnapshotAdaptiveChannelScores(candidates ...map[string]map[string][]int) []AdaptiveChannelScoreGroup {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)

	type groupModel struct{ group, model string }
	grouped := make(map[groupModel][]adaptiveChannelAggregate)

	adaptiveChannelWindows.RLock()
	for key, window := range adaptiveChannelWindows.windows {
		gm := groupModel{key.group, key.model}
		grouped[gm] = append(grouped[gm], window.aggregate(key.channelID, cutoff, minute))
	}
	adaptiveChannelWindows.RUnlock()

	var candidateSet map[string]map[string][]int
	if len(candidates) > 0 {
		candidateSet = candidates[0]
	}

	reports := make([]AdaptiveChannelScoreGroup, 0, len(grouped))
	probeMultipliers := adaptiveChannelProbeMultipliers(setting, setting.ProbeModel)
	for gm, all := range grouped {
		seen := make(map[int]struct{}, len(all))
		for _, agg := range all {
			seen[agg.channelID] = struct{}{}
		}
		if models := candidateSet[gm.group]; models != nil {
			for _, channelID := range models[gm.model] {
				if _, ok := seen[channelID]; ok {
					continue
				}
				seen[channelID] = struct{}{}
				all = append(all, adaptiveChannelAggregate{channelID: channelID})
			}
		}
		assignments := adaptiveChannelAssignments(setting, all, probeMultipliers)

		report := AdaptiveChannelScoreGroup{Group: gm.group, Model: gm.model}
		for _, agg := range all {
			snapshot := AdaptiveChannelScoreSnapshot{
				ChannelID:      agg.channelID,
				Requests:       agg.requests,
				Successes:      agg.successes,
				ServerErrors:   agg.serverErrors,
				AvgFirstByteMs: agg.avgFirstByteMs(),
			}
			if assignment, ok := assignments[agg.channelID]; ok {
				snapshot.Scored = true
				snapshot.Multiplier = assignment.multiplier
				snapshot.EffectiveSource = assignment.source
				if assignment.source == adaptiveChannelScoreSourceProbe {
					snapshot.ProbeModel = setting.ProbeModel
				}
			}
			report.Channels = append(report.Channels, snapshot)
		}
		slices.SortFunc(report.Channels, func(a, b AdaptiveChannelScoreSnapshot) int {
			return a.ChannelID - b.ChannelID
		})
		reports = append(reports, report)
	}
	slices.SortFunc(reports, func(a, b AdaptiveChannelScoreGroup) int {
		if a.Group != b.Group {
			return strings.Compare(a.Group, b.Group)
		}
		return strings.Compare(a.Model, b.Model)
	})
	return reports
}

// SnapshotAdaptiveChannelProbeScores reports the baseline the selector can use
// for a business model that has not reached MinSamples. It only contains the
// currently configured probe model; disabling probes or changing the model
// makes the old baseline invisible immediately.
func SnapshotAdaptiveChannelProbeScores() []AdaptiveChannelProbeSnapshot {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || setting.ProbeModel == "" {
		return nil
	}
	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)
	aggregates := adaptiveChannelProbeAggregates(setting.ProbeModel, cutoff, minute)
	multipliers := adaptiveChannelProbeMultipliers(setting, setting.ProbeModel)

	snapshots := make([]AdaptiveChannelProbeSnapshot, 0, len(aggregates))
	for _, agg := range aggregates {
		snapshot := AdaptiveChannelProbeSnapshot{
			ChannelID:      agg.channelID,
			ProbeModel:     setting.ProbeModel,
			Requests:       agg.requests,
			Successes:      agg.successes,
			ServerErrors:   agg.serverErrors,
			AvgFirstByteMs: agg.avgFirstByteMs(),
		}
		if multiplier, ok := multipliers[agg.channelID]; ok {
			snapshot.Scored = true
			snapshot.Multiplier = multiplier
			snapshot.Source = adaptiveChannelScoreSourceProbe
		}
		snapshots = append(snapshots, snapshot)
	}
	slices.SortFunc(snapshots, func(a, b AdaptiveChannelProbeSnapshot) int {
		return a.ChannelID - b.ChannelID
	})
	return snapshots
}

const adaptiveChannelProbeStateSyncInterval = 30 * time.Second

// StartAdaptiveChannelProbeStateSync lets every node read the latest persisted
// probe windows without putting a database query on the selector path. Only the
// assigned task runner writes the state; read-only nodes refresh it here.
func StartAdaptiveChannelProbeStateSync() {
	gopool.Go(func() {
		syncAdaptiveChannelProbeStateFromTask()
		ticker := time.NewTicker(adaptiveChannelProbeStateSyncInterval)
		defer ticker.Stop()
		for range ticker.C {
			syncAdaptiveChannelProbeStateFromTask()
		}
	})
}

func syncAdaptiveChannelProbeStateFromTask() {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || setting.ProbeModel == "" {
		return
	}
	task, err := model.GetLatestFinishedSystemTask(model.SystemTaskTypeAdaptiveChannelProbe)
	if err != nil || task == nil || task.Result == "" {
		return
	}
	var result struct {
		ProbeModel string                       `json:"probe_model"`
		Windows    []AdaptiveChannelProbeWindow `json:"windows"`
	}
	if err := common.UnmarshalJsonStr(task.Result, &result); err != nil {
		return
	}
	if result.ProbeModel != setting.ProbeModel {
		return
	}
	MergeAdaptiveChannelProbeState(result.Windows)
}
