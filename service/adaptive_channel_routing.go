package service

import (
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
	windows      map[adaptiveChannelKey]*adaptiveChannelWindow
	lastPruneAt  int64
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

// AdaptiveChannelWeightMultipliers returns neutral-to-bounded multipliers for
// channels with enough recent observations. Static priority tiers stay intact.
func AdaptiveChannelWeightMultipliers(group, model string) map[int]float64 {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || group == "" || model == "" {
		return nil
	}

	minute := time.Now().Unix() / 60
	cutoff := minute - int64(setting.WindowMinutes-1)
	result := make(map[int]float64)
	adaptiveChannelWindows.RLock()
	defer adaptiveChannelWindows.RUnlock()
	for key, window := range adaptiveChannelWindows.windows {
		if key.group != group || key.model != model {
			continue
		}
		var requests, successes, serverErrors, firstByteTotal, firstByteSamples int64
		for _, bucket := range window.buckets {
			if bucket.minute < cutoff || bucket.minute > minute {
				continue
			}
			requests += bucket.requests
			successes += bucket.successes
			serverErrors += bucket.serverErrors
			firstByteTotal += bucket.firstByteTotal
			firstByteSamples += bucket.firstByteSample
		}
		if requests < int64(setting.MinSamples) {
			continue
		}
		score := adaptiveChannelScore(requests, successes, serverErrors, firstByteTotal, firstByteSamples)
		result[key.channelID] = 0.5 + score/100
	}
	if len(result) == 0 {
		return nil
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
