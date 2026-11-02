package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	probeOpenAIStreamFirst = "data: {\"id\":\"chatcmpl-probe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"
	probeOpenAIStreamUsage = "data: {\"id\":\"chatcmpl-probe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n"
	probeOpenAIStreamDone  = "data: [DONE]\n\n"
	probeOpenAIStream      = probeOpenAIStreamFirst + probeOpenAIStreamUsage + probeOpenAIStreamDone
)

func setupProbeControllerDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var dialector gorm.Dialector
	var databaseType common.DatabaseType
	switch dialect {
	case "sqlite":
		dialector = sqlite.Open(":memory:")
		databaseType = common.DatabaseTypeSQLite
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		dialector = mysql.Open(dsn)
		databaseType = common.DatabaseTypeMySQL
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		dialector = postgres.Open(dsn)
		databaseType = common.DatabaseTypePostgreSQL
	default:
		t.Fatalf("unknown dialect %s", dialect)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster := common.IsMasterNode
	previousMemoryCache := common.MemoryCacheEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousRedis := common.RedisEnabled
	previousStreamingTimeout := constant.StreamingTimeout
	model.DB, model.LOG_DB = db, db
	common.SetMainDatabaseType(databaseType)
	common.SetLogDatabaseType(databaseType)
	common.IsMasterNode = true
	common.MemoryCacheEnabled = false
	common.LogConsumeEnabled = true
	common.RedisEnabled = false
	constant.StreamingTimeout = 60
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetMainDatabaseType(previousMain)
		common.SetLogDatabaseType(previousLog)
		common.IsMasterNode = previousMaster
		common.MemoryCacheEnabled = previousMemoryCache
		common.LogConsumeEnabled = previousLogConsume
		common.RedisEnabled = previousRedis
		constant.StreamingTimeout = previousStreamingTimeout
		require.NoError(t, db.Migrator().DropTable(&model.User{}, &model.Channel{}, &model.Log{}, &model.SystemTask{}, &model.SystemTaskLock{}))
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Log{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	query := "SELECT version()"
	if dialect == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database version: %s", version)
	return db
}

func createProbeRootUser(t *testing.T, db *gorm.DB, suffix string) *model.User {
	t.Helper()
	user := &model.User{
		Username: "probe-root-" + suffix,
		Password: "not-used",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100000000,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

var probeTestChannelID atomic.Int64

func createProbeChannel(t *testing.T, db *gorm.DB, baseURL, name, modelName string, configure func(*model.Channel)) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Id:       int(probeTestChannelID.Add(1)) + 100000,
		Type:     constant.ChannelTypeOpenAI,
		Key:      "sk-probe-test",
		Name:     name,
		Status:   common.ChannelStatusEnabled,
		BaseURL:  &baseURL,
		Models:   modelName,
		Group:    "default",
		Priority: common.GetPointer(int64(0)),
		Weight:   common.GetPointer(uint(1)),
		AutoBan:  common.GetPointer(1),
	}
	if configure != nil {
		configure(channel)
	}
	require.NoError(t, db.Create(channel).Error)
	return channel
}

func ensureProbeModelRatio(t *testing.T, modelName string) {
	t.Helper()
	previous := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previous))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios[modelName] = 1
	encoded, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
}

func updateProbeControllerSetting(t *testing.T, setting operation_setting.AdaptiveChannelRoutingSetting) {
	t.Helper()
	daysJSON, err := common.Marshal(setting.ProbeScheduleDays)
	require.NoError(t, err)
	values := map[string]string{
		"enabled":                 strconv.FormatBool(setting.Enabled),
		"window_minutes":          strconv.Itoa(setting.WindowMinutes),
		"min_samples":             strconv.Itoa(setting.MinSamples),
		"latency_mode":            setting.LatencyMode,
		"latency_exponent":        strconv.FormatFloat(setting.LatencyExponent, 'f', -1, 64),
		"multiplier_min":          strconv.FormatFloat(setting.MultiplierMin, 'f', -1, 64),
		"multiplier_max":          strconv.FormatFloat(setting.MultiplierMax, 'f', -1, 64),
		"probe_enabled":           strconv.FormatBool(setting.ProbeEnabled),
		"probe_model":             setting.ProbeModel,
		"probe_interval_seconds":  strconv.Itoa(setting.ProbeIntervalSeconds),
		"probe_max_requests":      strconv.Itoa(setting.ProbeMaxRequests),
		"probe_max_output_tokens": strconv.Itoa(setting.ProbeMaxOutputTokens),
		"probe_schedule_enabled":  strconv.FormatBool(setting.ProbeScheduleEnabled),
		"probe_schedule_days":     string(daysJSON),
		"probe_schedule_start":    setting.ProbeScheduleStart,
		"probe_schedule_end":      setting.ProbeScheduleEnd,
	}
	require.NoError(t, config.UpdateConfigFromMap(
		config.GlobalConfig.Get("adaptive_channel_routing_setting"),
		values,
	))
}

func setProbeControllerSetting(t *testing.T, setting operation_setting.AdaptiveChannelRoutingSetting) {
	t.Helper()
	previous := operation_setting.GetAdaptiveChannelRoutingSetting()
	t.Cleanup(func() { updateProbeControllerSetting(t, previous) })
	updateProbeControllerSetting(t, setting)
}

func probeStreamServer(t *testing.T, requests *atomic.Int64, bodies *[]map[string]any, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		if bodies != nil {
			body, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			if err := common.Unmarshal(body, &parsed); err != nil {
				t.Errorf("decode probe request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			*bodies = append(*bodies, parsed)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(probeOpenAIStream))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAdaptiveChannelProbeBoundsFinalUpstreamRequest(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	user := createProbeRootUser(t, db, "bounds")
	probeModel := "probe-bounds-model"
	ensureProbeModelRatio(t, probeModel)
	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 10,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 6, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	var mu sync.Mutex
	var bodies []map[string]any
	server := probeStreamServer(t, nil, &bodies, &mu)

	channel := createProbeChannel(t, db, server.URL, "probe-bounds", probeModel, func(channel *model.Channel) {
		channel.ModelMapping = common.GetPointer(fmt.Sprintf(`{%q:"upstream-model"}`, probeModel))
		channel.ParamOverride = common.GetPointer(`{"max_tokens":999999,"max_completion_tokens":888888,"n":8,"stream":false,"tools":[{"type":"function","function":{"name":"expensive"}}]}`)
	})

	result := testChannelWithOptions(context.Background(), channel, user.Id, probeModel, "", true, channelTestOptions{
		Probe: &channelTestProbeOptions{MaxOutputTokens: 16, LogLabel: adaptiveChannelProbeLogLabel, Group: "default"},
	})
	require.NoError(t, result.localErr, "probe local error: %v", result.newAPIError)
	require.Nil(t, result.newAPIError)
	require.Len(t, bodies, 1)
	assert.Equal(t, "upstream-model", bodies[0]["model"], "the probe follows the channel model mapping")
	assert.Equal(t, float64(16), bodies[0]["max_tokens"], "parameter overrides cannot raise the probe cap")
	assert.NotContains(t, bodies[0], "max_completion_tokens")
	assert.Equal(t, float64(1), bodies[0]["n"])
	assert.Equal(t, true, bodies[0]["stream"])
	assert.NotContains(t, bodies[0], "tools")
	streamOptions, ok := bodies[0]["stream_options"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, streamOptions["include_usage"])
	messages, ok := bodies[0]["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "hi", message["content"])
	assert.True(t, result.hasFirstByte)
	assert.False(t, result.firstByteAt.IsZero())
	assert.GreaterOrEqual(t, result.firstByteMs, int64(0))

	var logEntry model.Log
	require.NoError(t, db.Where("token_name = ?", adaptiveChannelProbeLogLabel).First(&logEntry).Error)
	assert.Equal(t, channel.Id, logEntry.ChannelId)
	assert.Equal(t, probeModel, logEntry.ModelName)
}

func TestAdaptiveChannelProbeMeasuresFirstPayloadNotHeaders(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	user := createProbeRootUser(t, db, "ttft")
	probeModel := "probe-ttft-model"
	ensureProbeModelRatio(t, probeModel)
	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 10,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 6, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	headersSent := make(chan time.Time, 1)
	releaseHeartbeat := make(chan struct{})
	heartbeatSent := make(chan time.Time, 1)
	releaseFirst := make(chan struct{})
	firstSent := make(chan time.Time, 1)
	releaseDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		headersSent <- time.Now()
		<-releaseHeartbeat
		heartbeatAt := time.Now()
		_, _ = w.Write([]byte(": heartbeat\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		heartbeatSent <- heartbeatAt
		<-releaseFirst
		firstAt := time.Now()
		_, _ = w.Write([]byte(probeOpenAIStreamFirst))
		if flusher != nil {
			flusher.Flush()
		}
		firstSent <- firstAt
		<-releaseDone
		_, _ = w.Write([]byte(probeOpenAIStreamUsage + probeOpenAIStreamDone))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)
	channel := createProbeChannel(t, db, server.URL, "probe-ttft", probeModel, nil)

	resultCh := make(chan testResult, 1)
	go func() {
		resultCh <- testChannelWithOptions(context.Background(), channel, user.Id, probeModel, "", true, channelTestOptions{
			Probe: &channelTestProbeOptions{MaxOutputTokens: 16, LogLabel: adaptiveChannelProbeLogLabel, Group: "default"},
		})
	}()

	headerAt := <-headersSent
	select {
	case result := <-resultCh:
		t.Fatalf("probe completed from headers alone: %+v", result)
	default:
	}
	close(releaseHeartbeat)
	heartbeatAt := <-heartbeatSent
	select {
	case result := <-resultCh:
		t.Fatalf("probe completed from an SSE heartbeat: %+v", result)
	default:
	}
	close(releaseFirst)
	firstAt := <-firstSent
	select {
	case result := <-resultCh:
		t.Fatalf("probe completed before the stream body finished: %+v", result)
	default:
	}
	close(releaseDone)
	result := <-resultCh
	require.NoError(t, result.localErr, "probe local error: %v", result.newAPIError)
	require.Nil(t, result.newAPIError)
	assert.False(t, result.firstByteAt.Before(firstAt), "TTFT is recorded at the first stream data event")
	assert.True(t, result.firstByteAt.After(heartbeatAt), "an SSE heartbeat is not the first data event")
	assert.True(t, result.firstByteAt.After(headerAt), "response headers alone are not the first byte")
	assert.GreaterOrEqual(t, result.firstByteMs, int64(0))
}

func TestPrepareProbeRequestBoundsFinalFormats(t *testing.T) {
	tests := []struct {
		name        string
		format      relaytypes.RelayFormat
		model       string
		channelType int
		input       string
		wantErr     string
		check       func(t *testing.T, body map[string]any)
	}{
		{
			name: "OpenAI chat", format: relaytypes.RelayFormatOpenAI, model: "chat-probe",
			input: `{"model":"chat-probe","messages":[{"role":"user","content":"long"}],"max_tokens":999,"max_completion_tokens":999,"n":8,"tools":[{"type":"function"}],"stream":false}`,
			check: func(t *testing.T, body map[string]any) {
				assert.Equal(t, float64(16), body["max_tokens"])
				assert.NotContains(t, body, "max_completion_tokens")
				assert.Equal(t, float64(1), body["n"])
				assert.Equal(t, true, body["stream"])
				assert.NotContains(t, body, "tools")
				options, ok := body["stream_options"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, true, options["include_usage"])
			},
		},
		{
			name: "reasoning OpenAI", format: relaytypes.RelayFormatOpenAI, model: "o3-mini",
			input: `{"model":"o3-mini","messages":[{"role":"user","content":"long"}],"max_completion_tokens":999,"max_tokens":999,"n":4}`,
			check: func(t *testing.T, body map[string]any) {
				assert.Equal(t, float64(16), body["max_completion_tokens"])
				assert.NotContains(t, body, "max_tokens")
				assert.Equal(t, float64(1), body["n"])
			},
		},
		{
			name: "Responses", format: relaytypes.RelayFormatOpenAIResponses, model: "responses-probe",
			input: `{"model":"responses-probe","input":"long","max_output_tokens":999,"max_tokens":999,"tools":[{"type":"web_search"}],"stream":false}`,
			check: func(t *testing.T, body map[string]any) {
				assert.Equal(t, float64(16), body["max_output_tokens"])
				assert.NotContains(t, body, "max_tokens")
				assert.NotContains(t, body, "tools")
				assert.Equal(t, true, body["stream"])
				assert.Equal(t, false, body["store"])
			},
		},
		{
			name: "native Gemini is rejected", format: relaytypes.RelayFormatGemini, model: "gemini-probe",
			input:   `{"generationConfig":{"maxOutputTokens":999}}`,
			wantErr: "probe protocol",
		},
		{
			name: "native Claude is rejected", format: relaytypes.RelayFormatClaude, model: "claude-probe",
			input:   `{"model":"claude-probe","messages":[{"role":"user","content":"hi"}],"max_tokens":999}`,
			wantErr: "probe protocol",
		},
		{
			name: "Codex is rejected", format: relaytypes.RelayFormatOpenAIResponses, model: "gpt-5-codex",
			channelType: constant.ChannelTypeCodex,
			input:       `{"model":"gpt-5-codex","input":"hi","max_output_tokens":999}`,
			wantErr:     "does not support bounded",
		},
		{
			name: "mapped media model is rejected", format: relaytypes.RelayFormatOpenAI, model: "gpt-image-1",
			input:   `{"model":"gpt-image-1","messages":[{"role":"user","content":"hi"}],"max_tokens":999}`,
			wantErr: "not a text model",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channelType := test.channelType
			if channelType == 0 {
				channelType = constant.ChannelTypeOpenAI
			}
			info := &relaycommon.RelayInfo{
				FinalRequestRelayFormat: test.format,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       channelType,
					UpstreamModelName: test.model,
				},
			}
			out, err := prepareProbeRequest(info, []byte(test.input), 16)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			var body map[string]any
			require.NoError(t, common.Unmarshal(out, &body))
			test.check(t, body)
		})
	}
}

func TestAdaptiveChannelProbeSupportsResponsesStreamUsage(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	user := createProbeRootUser(t, db, "responses")
	probeModel := "probe-responses-codex"
	ensureProbeModelRatio(t, probeModel)
	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 10,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 6, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	var mu sync.Mutex
	var bodies []map[string]any
	responsesStream := "" +
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-probe\",\"model\":\"probe-responses-codex\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-probe\",\"model\":\"probe-responses-codex\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := common.Unmarshal(body, &parsed); err != nil {
			t.Errorf("decode Responses probe request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, parsed)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(responsesStream))
	}))
	t.Cleanup(server.Close)
	channel := createProbeChannel(t, db, server.URL, "probe-responses", probeModel, nil)

	result := testChannelWithOptions(context.Background(), channel, user.Id, probeModel, "", true, channelTestOptions{
		Probe: &channelTestProbeOptions{MaxOutputTokens: 16, LogLabel: adaptiveChannelProbeLogLabel, Group: "default"},
	})
	require.NoError(t, result.localErr, "probe local error: %v", result.newAPIError)
	require.Nil(t, result.newAPIError)
	require.Len(t, bodies, 1)
	assert.Equal(t, float64(16), bodies[0]["max_output_tokens"])
	assert.Equal(t, true, bodies[0]["stream"])
	assert.Equal(t, false, bodies[0]["store"])
	assert.NotContains(t, bodies[0], "tools")
	assert.True(t, result.hasFirstByte)
}

func TestAdaptiveChannelProbeTaskRotatesAndStopsAtThreshold(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	createProbeRootUser(t, db, "rotate")
	probeModel := "probe-fair-model"
	ensureProbeModelRatio(t, probeModel)
	setting := operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 2, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	}
	setProbeControllerSetting(t, setting)

	var requests atomic.Int64
	server := probeStreamServer(t, &requests, nil, nil)
	for i := range 3 {
		createProbeChannel(t, db, server.URL, fmt.Sprintf("probe-rotate-%d", i), probeModel, nil)
	}

	first, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, first.Tested, "one round is capped by probe_max_requests")
	second, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, second.Tested)
	assert.EqualValues(t, 4, requests.Load(), "the two rounds sent exactly the configured number of probes")

	baselines := service.SnapshotAdaptiveChannelProbeScores()
	require.Len(t, baselines, 3)
	for _, baseline := range baselines {
		assert.GreaterOrEqual(t, baseline.Requests, int64(1), "fair rotation reaches every channel")
	}

	withLowerThreshold := setting
	withLowerThreshold.MinSamples = 1
	updateProbeControllerSetting(t, withLowerThreshold)
	third, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Zero(t, third.Tested, "channels that reached MinSamples are not probed again")
	assert.EqualValues(t, 4, requests.Load())
}

func TestAdaptiveChannelProbeSkipsChannelsWithEnoughLiveSamples(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	createProbeRootUser(t, db, "enough")
	probeModel := "probe-enough-model"
	ensureProbeModelRatio(t, probeModel)
	setting := operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 2, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	}
	setProbeControllerSetting(t, setting)

	var requests atomic.Int64
	server := probeStreamServer(t, &requests, nil, nil)
	channel := createProbeChannel(t, db, server.URL, "probe-enough", probeModel+",active-model", nil)
	for range 2 {
		service.RecordAdaptiveChannelOutcome("default", "active-model", channel.Id, http.StatusOK, true, 500, true)
	}

	summary, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Zero(t, summary.Tested)
	assert.Zero(t, requests.Load())
	assert.Empty(t, service.SnapshotAdaptiveChannelProbeScores())
}

func TestAdaptiveChannelProbeLoadsPersistedStateOnAnotherRunner(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	createProbeRootUser(t, db, "persisted")
	probeModel := "probe-persisted-model"
	ensureProbeModelRatio(t, probeModel)
	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 2,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 1, ProbeMaxOutputTokens: 16,
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	var requests atomic.Int64
	server := probeStreamServer(t, &requests, nil, nil)
	channel := createProbeChannel(t, db, server.URL, "probe-persisted", probeModel, nil)

	minute := time.Now().Unix() / 60
	persisted := adaptiveChannelProbeTaskResult{
		ProbeModel: probeModel,
		Windows: []service.AdaptiveChannelProbeWindow{{
			ProbeModel: probeModel,
			ChannelID:  channel.Id,
			Buckets: []service.AdaptiveChannelProbeBucket{{
				Minute: minute, Requests: 2, Successes: 2, FirstByteTotal: 1600, FirstByteSample: 2,
			}},
		}},
		Cursor: channel.Id,
	}
	encoded, err := common.Marshal(persisted)
	require.NoError(t, err)
	task, err := model.CreateSystemTask(model.SystemTaskTypeAdaptiveChannelProbe, nil, nil)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.SystemTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.SystemTaskStatusSucceeded, "result": string(encoded), "active_key": nil,
	}).Error)

	summary, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Zero(t, summary.Tested, "a new runner sees the persisted samples and does not re-probe")
	assert.Zero(t, requests.Load())
}

func TestAdaptiveChannelProbeScheduleBlocksOutsideWindow(t *testing.T) {
	previousClock := adaptiveProbeClock
	t.Cleanup(func() { adaptiveProbeClock = previousClock })
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	mondayMorning := time.Date(2026, 10, 26, 8, 0, 0, 0, beijing)
	adaptiveProbeClock = func() time.Time { return mondayMorning }

	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 10,
		ProbeEnabled: true, ProbeModel: "probe-schedule-model",
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 2, ProbeMaxOutputTokens: 16,
		ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1},
		ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00",
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	summary, err := runAdaptiveChannelProbeTask(context.Background())
	require.NoError(t, err)
	assert.Zero(t, summary.Candidates)
	assert.Zero(t, summary.Tested)
	assert.Equal(t, "outside probe schedule", summary.Reason)
	assert.False(t, adaptiveChannelProbeHandler{}.Enabled())
}

func TestAdaptiveChannelProbeScheduleStopsQueuedDispatchButFinishesInFlight(t *testing.T) {
	db := setupProbeControllerDB(t, "sqlite")
	createProbeRootUser(t, db, "schedule")
	probeModel := "probe-schedule-inflight"
	ensureProbeModelRatio(t, probeModel)

	previousClock := adaptiveProbeClock
	t.Cleanup(func() { adaptiveProbeClock = previousClock })
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, beijing)
	var clockMu sync.Mutex
	currentTime := monday.Add(9*time.Hour + 30*time.Minute)
	adaptiveProbeClock = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return currentTime
	}

	setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
		Enabled: true, WindowMinutes: 30, MinSamples: 10,
		ProbeEnabled: true, ProbeModel: probeModel,
		ProbeIntervalSeconds: 60, ProbeMaxRequests: 3, ProbeMaxOutputTokens: 16,
		ProbeScheduleEnabled: true, ProbeScheduleDays: []int{1},
		ProbeScheduleStart: "09:00", ProbeScheduleEnd: "10:00",
		LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
		LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
	})

	var requests atomic.Int64
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(probeOpenAIStream))
	}))
	t.Cleanup(server.Close)

	channels := make([]*model.Channel, 0, 3)
	for i := range 3 {
		channels = append(channels, createProbeChannel(t, db, server.URL, fmt.Sprintf("probe-schedule-%d", i), probeModel, nil))
	}

	resultCh := make(chan struct {
		summary adaptiveChannelProbeSummary
		err     error
	}, 1)
	go func() {
		summary, err := runAdaptiveChannelProbeTask(context.Background())
		resultCh <- struct {
			summary adaptiveChannelProbeSummary
			err     error
		}{summary: summary, err: err}
	}()

	for i := range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d probe request(s) started", i)
		}
	}
	select {
	case <-started:
		t.Fatal("a third probe was dispatched before the window closed")
	default:
	}

	clockMu.Lock()
	currentTime = monday.Add(10 * time.Hour)
	clockMu.Unlock()
	releaseAll()

	var result struct {
		summary adaptiveChannelProbeSummary
		err     error
	}
	select {
	case result = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("probe round did not finish")
	}
	require.NoError(t, result.err)
	summary := result.summary
	assert.Equal(t, 3, summary.Candidates)
	assert.Equal(t, 2, summary.Tested, "queued probes must not be dispatched after the window closes")
	assert.Equal(t, 2, summary.Succeeded, "in-flight probes still record their real outcome")
	assert.Zero(t, summary.Failed)
	assert.EqualValues(t, 2, requests.Load())

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("token_name = ?", adaptiveChannelProbeLogLabel).Find(&logs).Error)
	assert.Len(t, logs, 2, "both in-flight probes wrote real consume logs")
	for _, channel := range channels {
		key := adaptiveChannelProbeBackoffKey{model: probeModel, channelID: channel.Id}
		assert.Zero(t, adaptiveProbeBackoffRemaining(key, time.Now()), "closing the window does not add backoff")
	}
}

func TestAdaptiveChannelProbeSystemTaskCompletesAndPersistsLogs(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := setupProbeControllerDB(t, dialect)
			createProbeRootUser(t, db, dialect)
			probeModel := "probe-db-" + dialect
			ensureProbeModelRatio(t, probeModel)
			setProbeControllerSetting(t, operation_setting.AdaptiveChannelRoutingSetting{
				Enabled: true, WindowMinutes: 30, MinSamples: 2,
				ProbeEnabled: true, ProbeModel: probeModel,
				ProbeIntervalSeconds: 60, ProbeMaxRequests: 1, ProbeMaxOutputTokens: 16,
				LatencyMode:     operation_setting.AdaptiveLatencyModeAbsolute,
				LatencyExponent: 2, MultiplierMin: 0.5, MultiplierMax: 1.5,
			})

			server := probeStreamServer(t, nil, nil, nil)
			channel := createProbeChannel(t, db, server.URL, "probe-db", probeModel, nil)

			task, err := model.CreateSystemTask(model.SystemTaskTypeAdaptiveChannelProbe, nil, nil)
			require.NoError(t, err)
			claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "probe-test-runner", time.Now().Unix()+60)
			require.NoError(t, err)
			require.True(t, ok)

			adaptiveChannelProbeHandler{}.Run(context.Background(), claimed, "probe-test-runner")

			finished, err := model.GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			require.NotNil(t, finished)
			assert.Equal(t, model.SystemTaskStatusSucceeded, finished.Status)
			assert.Contains(t, finished.Result, `"tested":1`)
			assert.Contains(t, finished.Result, `"windows"`, "the task result persists probe windows for other nodes")
			assert.Contains(t, finished.Result, `"probe_model":"`+probeModel+`"`)

			var logEntry model.Log
			require.NoError(t, db.Where("token_name = ?", adaptiveChannelProbeLogLabel).First(&logEntry).Error)
			assert.Equal(t, channel.Id, logEntry.ChannelId)
			assert.Equal(t, probeModel, logEntry.ModelName)
			assert.Equal(t, "default", logEntry.Group)

			baselines := service.SnapshotAdaptiveChannelProbeScores()
			require.Len(t, baselines, 1)
			assert.EqualValues(t, 1, baselines[0].Requests)
			assert.Equal(t, channel.Id, baselines[0].ChannelID)
		})
	}
}
