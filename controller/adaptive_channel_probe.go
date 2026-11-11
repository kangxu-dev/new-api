package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	adaptiveChannelProbeLogLabel  = "自适应评分补样"
	adaptiveChannelProbeWorkers   = 2
	adaptiveChannelProbeTimeout   = 30 * time.Second
	adaptiveChannelProbeMaxDelay  = 30 * time.Minute
	adaptiveChannelProbeBaseDelay = time.Minute
)

var adaptiveProbeNonTextModelFragments = []string{
	"embed",
	"rerank",
	"whisper",
	"transcri",
	"tts",
	"speech",
	"audio",
	"image",
	"imagen",
	"dall-e",
	"flux",
	"stable-diffusion",
	"sd3",
	"recraft",
	"ideogram",
	"bria",
	"seedream",
	"seedance",
	"sora",
	"video",
	"veo",
	"runway",
	"pika",
	"luma",
	"midjourney",
	"suno",
	"kling",
	"jimeng",
	"vidu",
	"wanx",
	"cogvideo",
}

type adaptiveChannelProbeBackoffKey struct {
	model     string
	channelID int
}

type adaptiveChannelProbeBackoffState struct {
	failures    int
	nextAttempt time.Time
}

type adaptiveChannelProbeBackoffEntry struct {
	ChannelID       int   `json:"channel_id"`
	Failures        int   `json:"failures"`
	NextAttemptUnix int64 `json:"next_attempt_unix"`
}

var adaptiveChannelProbeBackoffs = struct {
	sync.Mutex
	entries map[adaptiveChannelProbeBackoffKey]adaptiveChannelProbeBackoffState
}{entries: make(map[adaptiveChannelProbeBackoffKey]adaptiveChannelProbeBackoffState)}

var adaptiveChannelProbeCursor = struct {
	sync.Mutex
	channelID int
}{}

// adaptiveProbeClock is a small domain-level seam so tests can advance the
// weekly schedule boundary without sleeping. Production always uses time.Now.
var adaptiveProbeClock = time.Now

type adaptiveChannelProbeTaskResult struct {
	Summary    adaptiveChannelProbeSummary          `json:"summary"`
	ProbeModel string                               `json:"probe_model"`
	Windows    []service.AdaptiveChannelProbeWindow `json:"windows,omitempty"`
	Backoff    []adaptiveChannelProbeBackoffEntry   `json:"backoff,omitempty"`
	Cursor     int                                  `json:"cursor,omitempty"`
}

type adaptiveChannelProbeSummary struct {
	Candidates int    `json:"candidates"`
	Tested     int    `json:"tested"`
	Succeeded  int    `json:"succeeded"`
	Failed     int    `json:"failed"`
	Skipped    int    `json:"skipped"`
	Reason     string `json:"reason,omitempty"`
}

type adaptiveChannelProbeCandidate struct {
	channel *model.Channel
	samples int64
}

type adaptiveChannelProbeResult struct {
	channelID int
	result    testResult
	canceled  bool
}

type adaptiveChannelProbeHandler struct{}

func (adaptiveChannelProbeHandler) Type() string { return model.SystemTaskTypeAdaptiveChannelProbe }

func (adaptiveChannelProbeHandler) Enabled() bool {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	return setting.Enabled && setting.ProbeEnabled && setting.ProbeModel != "" &&
		setting.ProbeScheduleAllows(adaptiveProbeClock())
}

func (adaptiveChannelProbeHandler) Interval() time.Duration {
	seconds := operation_setting.GetAdaptiveChannelRoutingSetting().ProbeIntervalSeconds
	if seconds < operation_setting.AdaptiveProbeIntervalSecondsMin {
		seconds = operation_setting.AdaptiveProbeIntervalSecondsDefault
	}
	return time.Duration(seconds) * time.Second
}

func (adaptiveChannelProbeHandler) NewPayload() any { return nil }

func (adaptiveChannelProbeHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary, err := runAdaptiveChannelProbeTask(ctx)
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	result := adaptiveChannelProbeTaskResult{
		Summary:    summary,
		ProbeModel: setting.ProbeModel,
		Windows:    service.ExportAdaptiveChannelProbeState(setting.ProbeModel),
		Backoff:    adaptiveProbeBackoffSnapshot(setting.ProbeModel),
		Cursor:     adaptiveProbeCursorValue(),
	}
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, result, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, result, nil)
}

// adaptiveProbeModelIsText rejects known non-text model families before any
// request is sent. The check is intentionally conservative: an administrator
// can still configure a model whose name is unfamiliar, but an obviously
// multimodal or media model is never probed.
func adaptiveProbeModelIsText(modelName string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	if lower == "" {
		return false
	}
	for _, fragment := range adaptiveProbeNonTextModelFragments {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	return true
}

func adaptiveProbeSettingsEqual(a, b operation_setting.AdaptiveChannelRoutingSetting) bool {
	return a.Enabled == b.Enabled &&
		a.ProbeEnabled == b.ProbeEnabled &&
		a.ProbeModel == b.ProbeModel &&
		a.ProbeIntervalSeconds == b.ProbeIntervalSeconds &&
		a.ProbeMaxRequests == b.ProbeMaxRequests &&
		a.ProbeMaxOutputTokens == b.ProbeMaxOutputTokens &&
		a.ProbeScheduleEnabled == b.ProbeScheduleEnabled &&
		slices.Equal(a.ProbeScheduleDays, b.ProbeScheduleDays) &&
		a.ProbeScheduleStart == b.ProbeScheduleStart &&
		a.ProbeScheduleEnd == b.ProbeScheduleEnd
}

func adaptiveProbeSettingStillActive(round operation_setting.AdaptiveChannelRoutingSetting) bool {
	current := operation_setting.GetAdaptiveChannelRoutingSetting()
	return current.Enabled && current.ProbeEnabled && adaptiveProbeSettingsEqual(round, current)
}

// adaptiveProbeChannelSupported restricts probes to channel implementations
// whose final upstream body is the canonical OpenAI chat/Responses protocol.
// Native Claude/Gemini, Codex (which deliberately drops max_output_tokens and
// requires instructions), and other custom adaptors are skipped rather than
// receiving a guessed token limit.
func adaptiveProbeChannelSupported(channel *model.Channel) bool {
	if channel == nil || slices.Contains(unsupportedTestChannelTypes, channel.Type) {
		return false
	}
	return adaptiveProbeChannelTypeSupported(channel.Type)
}

func adaptiveProbeChannelTypeSupported(channelType int) bool {
	apiType, known := common.ChannelType2APIType(channelType)
	if !known {
		return false
	}
	switch apiType {
	case constant.APITypeOpenAI,
		constant.APITypeNewAPI,
		constant.APITypeSub2API,
		constant.APITypeOpenRouter,
		constant.APITypeXinference:
		return true
	default:
		return false
	}
}

func adaptiveProbeChannelStillEligible(channelID int, probeModel string) bool {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled {
		return false
	}
	return adaptiveProbeChannelSupported(channel) && adaptiveProbeChannelSupportsModel(channel, probeModel)
}

func adaptiveProbeBackoffRemaining(key adaptiveChannelProbeBackoffKey, now time.Time) time.Duration {
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	state, ok := adaptiveChannelProbeBackoffs.entries[key]
	if !ok || !state.nextAttempt.After(now) {
		return 0
	}
	return state.nextAttempt.Sub(now)
}

func noteAdaptiveProbeFailure(key adaptiveChannelProbeBackoffKey, statusCode int) {
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	state := adaptiveChannelProbeBackoffs.entries[key]
	state.failures++
	delay := adaptiveChannelProbeBaseDelay
	if statusCode == 429 {
		delay = 5 * time.Minute
	}
	for i := 1; i < state.failures; i++ {
		delay *= 2
		if delay >= adaptiveChannelProbeMaxDelay {
			break
		}
	}
	if delay > adaptiveChannelProbeMaxDelay {
		delay = adaptiveChannelProbeMaxDelay
	}
	state.nextAttempt = time.Now().Add(delay)
	adaptiveChannelProbeBackoffs.entries[key] = state
}

func clearAdaptiveProbeBackoff(key adaptiveChannelProbeBackoffKey) {
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	delete(adaptiveChannelProbeBackoffs.entries, key)
}

func adaptiveProbeBackoffSnapshot(probeModel string) []adaptiveChannelProbeBackoffEntry {
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	entries := make([]adaptiveChannelProbeBackoffEntry, 0, len(adaptiveChannelProbeBackoffs.entries))
	for key, state := range adaptiveChannelProbeBackoffs.entries {
		if key.model != probeModel || !state.nextAttempt.After(time.Now()) {
			continue
		}
		entries = append(entries, adaptiveChannelProbeBackoffEntry{
			ChannelID:       key.channelID,
			Failures:        state.failures,
			NextAttemptUnix: state.nextAttempt.Unix(),
		})
	}
	slices.SortFunc(entries, func(a, b adaptiveChannelProbeBackoffEntry) int {
		return a.ChannelID - b.ChannelID
	})
	return entries
}

func mergeAdaptiveProbeBackoff(probeModel string, entries []adaptiveChannelProbeBackoffEntry) {
	now := time.Now()
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	for _, entry := range entries {
		if entry.ChannelID <= 0 || entry.NextAttemptUnix <= 0 {
			continue
		}
		key := adaptiveChannelProbeBackoffKey{model: probeModel, channelID: entry.ChannelID}
		state := adaptiveChannelProbeBackoffs.entries[key]
		if entry.Failures > state.failures {
			state.failures = entry.Failures
		}
		next := time.Unix(entry.NextAttemptUnix, 0)
		if next.After(state.nextAttempt) {
			state.nextAttempt = next
		}
		if state.nextAttempt.After(now) {
			adaptiveChannelProbeBackoffs.entries[key] = state
		}
	}
}

func pruneAdaptiveProbeBackoffs(probeModel string, now time.Time) {
	adaptiveChannelProbeBackoffs.Lock()
	defer adaptiveChannelProbeBackoffs.Unlock()
	for key, state := range adaptiveChannelProbeBackoffs.entries {
		if key.model != probeModel || state.nextAttempt.Before(now.Add(-time.Hour)) {
			delete(adaptiveChannelProbeBackoffs.entries, key)
		}
	}
	if len(adaptiveChannelProbeBackoffs.entries) > 4096 {
		adaptiveChannelProbeBackoffs.entries = make(map[adaptiveChannelProbeBackoffKey]adaptiveChannelProbeBackoffState)
	}
}

func adaptiveProbeCursorValue() int {
	adaptiveChannelProbeCursor.Lock()
	defer adaptiveChannelProbeCursor.Unlock()
	return adaptiveChannelProbeCursor.channelID
}

func setAdaptiveProbeCursor(channelID int) {
	adaptiveChannelProbeCursor.Lock()
	defer adaptiveChannelProbeCursor.Unlock()
	adaptiveChannelProbeCursor.channelID = channelID
}

func loadAdaptiveProbeTaskState() {
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || setting.ProbeModel == "" {
		return
	}
	task, err := model.GetLatestFinishedSystemTask(model.SystemTaskTypeAdaptiveChannelProbe)
	if err != nil || task == nil || task.Result == "" {
		return
	}
	var result adaptiveChannelProbeTaskResult
	if err := common.UnmarshalJsonStr(task.Result, &result); err != nil {
		return
	}
	if result.ProbeModel != setting.ProbeModel {
		return
	}
	service.MergeAdaptiveChannelProbeState(result.Windows)
	mergeAdaptiveProbeBackoff(result.ProbeModel, result.Backoff)
	setAdaptiveProbeCursor(result.Cursor)
}

func adaptiveProbeChannelSupportsModel(channel *model.Channel, probeModel string) bool {
	return slices.ContainsFunc(channel.GetModels(), func(configured string) bool {
		return strings.TrimSpace(configured) == probeModel
	})
}

// runAdaptiveChannelProbeTask performs one bounded probe round. It enumerates
// enabled channels from the database, probes only channels that explicitly list
// the configured probe model, prioritises the least-sampled channels, and fills
// each channel's scoring window up to ProbeWindowCapacity.
func runAdaptiveChannelProbeTask(ctx context.Context) (adaptiveChannelProbeSummary, error) {
	summary := adaptiveChannelProbeSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	setting := operation_setting.GetAdaptiveChannelRoutingSetting()
	if !setting.Enabled || !setting.ProbeEnabled || setting.ProbeModel == "" {
		summary.Reason = "probe sampling is disabled"
		return summary, nil
	}
	if !adaptiveProbeModelIsText(setting.ProbeModel) {
		summary.Reason = "configured probe model is not a text model"
		return summary, nil
	}
	now := adaptiveProbeClock()
	if !setting.ProbeScheduleAllows(now) {
		summary.Reason = "outside probe schedule"
		return summary, nil
	}
	loadAdaptiveProbeTaskState()
	pruneAdaptiveProbeBackoffs(setting.ProbeModel, time.Now())

	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		summary.Reason = "no channel test user is available"
		common.SysLog(fmt.Sprintf("adaptive channel probe skipped: %v", err))
		return summary, nil
	}

	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return summary, err
	}

	candidates := make([]adaptiveChannelProbeCandidate, 0, len(channels))
	for _, channel := range channels {
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if len(channel.GetGroups()) == 0 || !adaptiveProbeChannelSupportsModel(channel, setting.ProbeModel) {
			continue
		}
		if !adaptiveProbeChannelSupported(channel) {
			continue
		}
		if !service.AdaptiveChannelNeedsProbeBaseline(channel.Id) {
			continue
		}
		samples := service.AdaptiveChannelProbeRequestCount(setting.ProbeModel, channel.Id)
		// Top the baseline up to what the scoring window can hold rather than
		// stopping at MinSamples. The window rolls, so a baseline parked on the
		// threshold drops below it every time its oldest sample ages out, and
		// routing silently reverts to neutral until the next round refills it.
		if samples >= setting.ProbeWindowCapacity() {
			continue
		}
		key := adaptiveChannelProbeBackoffKey{model: setting.ProbeModel, channelID: channel.Id}
		if adaptiveProbeBackoffRemaining(key, now) > 0 {
			summary.Skipped++
			continue
		}
		candidates = append(candidates, adaptiveChannelProbeCandidate{channel: channel, samples: samples})
	}
	summary.Candidates = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}

	cursor := adaptiveProbeCursorValue()
	slices.SortFunc(candidates, func(a, b adaptiveChannelProbeCandidate) int {
		if a.samples != b.samples {
			if a.samples < b.samples {
				return -1
			}
			return 1
		}
		// Round-robin within equal sample counts so a small channel ID cannot
		// monopolise the limited probe budget forever.
		aAfterCursor := a.channel.Id > cursor
		bAfterCursor := b.channel.Id > cursor
		if aAfterCursor != bAfterCursor {
			if aAfterCursor {
				return -1
			}
			return 1
		}
		return a.channel.Id - b.channel.Id
	})
	if len(candidates) > setting.ProbeMaxRequests {
		candidates = candidates[:setting.ProbeMaxRequests]
	}
	setAdaptiveProbeCursor(candidates[len(candidates)-1].channel.Id)

	summary, err = runAdaptiveChannelProbes(ctx, setting, testUserID, candidates, summary)
	return summary, err
}

func runAdaptiveChannelProbes(ctx context.Context, setting operation_setting.AdaptiveChannelRoutingSetting, testUserID int, candidates []adaptiveChannelProbeCandidate, summary adaptiveChannelProbeSummary) (adaptiveChannelProbeSummary, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan *model.Channel)
	results := make(chan adaptiveChannelProbeResult)
	stopDispatch := make(chan struct{})
	var stopDispatchOnce sync.Once
	stopNewDispatch := func() {
		stopDispatchOnce.Do(func() { close(stopDispatch) })
	}
	workerCount := min(adaptiveChannelProbeWorkers, len(candidates))

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for channel := range jobs {
				if ctx.Err() != nil {
					return
				}
				current := operation_setting.GetAdaptiveChannelRoutingSetting()
				if !current.Enabled || !current.ProbeEnabled || !adaptiveProbeSettingsEqual(setting, current) {
					cancel()
					stopNewDispatch()
					return
				}
				// The weekly window only stops future dispatch. In-flight
				// requests keep their own context and are allowed to finish.
				if !current.ProbeScheduleAllows(adaptiveProbeClock()) {
					stopNewDispatch()
					return
				}
				if !adaptiveProbeChannelStillEligible(channel.Id, setting.ProbeModel) {
					continue
				}
				probeCtx, probeCancel := context.WithTimeout(ctx, adaptiveChannelProbeTimeout)
				group := ""
				if groups := channel.GetGroups(); len(groups) > 0 {
					group = groups[0]
				}
				result := testChannelWithOptions(probeCtx, channel, testUserID, setting.ProbeModel, "", true, channelTestOptions{
					Probe: &channelTestProbeOptions{
						MaxOutputTokens: setting.ProbeMaxOutputTokens,
						LogLabel:        adaptiveChannelProbeLogLabel,
						Group:           group,
					},
				})
				canceled := ctx.Err() != nil
				probeCancel()
				results <- adaptiveChannelProbeResult{channelID: channel.Id, result: result, canceled: canceled}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case <-ctx.Done():
				return
			case <-stopDispatch:
				return
			case jobs <- candidate.channel:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	for probeResult := range results {
		if probeResult.canceled {
			continue
		}
		// Settings or the channel may have changed while the request was in
		// flight. Discard the result instead of recording it under the old
		// probe model or a channel the admin just disabled.
		if !adaptiveProbeSettingStillActive(setting) || !adaptiveProbeChannelStillEligible(probeResult.channelID, setting.ProbeModel) {
			continue
		}
		summary.Tested++
		key := adaptiveChannelProbeBackoffKey{model: setting.ProbeModel, channelID: probeResult.channelID}
		if probeResult.result.newAPIError == nil && probeResult.result.localErr == nil {
			service.RecordAdaptiveChannelProbeOutcome(setting.ProbeModel, probeResult.channelID, probeResult.result.upstreamStatusCode, true, probeResult.result.firstByteMs, probeResult.result.hasFirstByte)
			clearAdaptiveProbeBackoff(key)
			summary.Succeeded++
			continue
		}

		statusCode := probeResult.result.upstreamStatusCode
		if statusCode >= 500 && statusCode <= 599 {
			service.RecordAdaptiveChannelProbeOutcome(setting.ProbeModel, probeResult.channelID, statusCode, false, 0, false)
		}
		noteAdaptiveProbeFailure(key, statusCode)
		summary.Failed++
	}
	return summary, nil
}
