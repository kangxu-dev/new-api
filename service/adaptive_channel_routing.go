package service

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const adaptiveChannelBucketCount = 60

type adaptiveChannelKey struct {
	group     string
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

// AdaptiveChannelWeightMultipliers returns neutral-to-bounded multipliers for
// channels with enough recent observations. Static priority tiers stay intact.
func AdaptiveChannelWeightMultipliers(group, model string) map[int]float64 {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || group == "" || model == "" {
		return nil
	}

	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)
	aggregates := make([]adaptiveChannelAggregate, 0, 4)

	adaptiveChannelWindows.RLock()
	for key, window := range adaptiveChannelWindows.windows {
		if key.group != group || key.model != model {
			continue
		}
		agg := window.aggregate(key.channelID, cutoff, minute)
		if agg.requests < int64(setting.MinSamples) {
			continue
		}
		aggregates = append(aggregates, agg)
	}
	adaptiveChannelWindows.RUnlock()

	return adaptiveChannelWeightMultipliers(setting, aggregates)
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
	// Scored reports whether this channel reached MinSamples inside the window
	// and therefore carries a multiplier. An unscored channel keeps its
	// configured weight, which is why an inert score looks like no score at all.
	Scored     bool    `json:"scored"`
	Multiplier float64 `json:"multiplier"`
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
func SnapshotAdaptiveChannelScores() []AdaptiveChannelScoreGroup {
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

	reports := make([]AdaptiveChannelScoreGroup, 0, len(grouped))
	for gm, all := range grouped {
		scored := make([]adaptiveChannelAggregate, 0, len(all))
		for _, agg := range all {
			if agg.requests >= int64(setting.MinSamples) {
				scored = append(scored, agg)
			}
		}
		multipliers := adaptiveChannelWeightMultipliers(setting, scored)

		report := AdaptiveChannelScoreGroup{Group: gm.group, Model: gm.model}
		for _, agg := range all {
			snapshot := AdaptiveChannelScoreSnapshot{
				ChannelID:      agg.channelID,
				Requests:       agg.requests,
				Successes:      agg.successes,
				ServerErrors:   agg.serverErrors,
				AvgFirstByteMs: agg.avgFirstByteMs(),
			}
			if multiplier, ok := multipliers[agg.channelID]; ok {
				snapshot.Scored = true
				snapshot.Multiplier = multiplier
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
