package model

import (
	"errors"
	"maps"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestRequestPolicyDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "policy_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousSnapshot := DB, common.MainDatabaseType(), CurrentRequestPolicy()
			common.OptionMapRWMutex.Lock()
			previousOptions := maps.Clone(common.OptionMap)
			common.OptionMap = maps.Clone(previousSnapshot.Options)
			common.OptionMapRWMutex.Unlock()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Option{}))
				for k, v := range previousSnapshot.Options {
					require.NoError(t, updateOptionMap(k, v))
				}
				requestPolicySnapshot.Store(previousSnapshot)
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			rules := `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"ttl_seconds":0,"skip_retry_on_failure":false,"include_using_group":false,"param_override_template":{"temperature":0},"future_field":{"enabled":false}}]`
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{
				"RetryTimes": "2", "channel_affinity_setting.rules": rules,
				"adaptive_channel_routing_setting.enabled":        "true",
				"adaptive_channel_routing_setting.window_minutes": "15",
				"adaptive_channel_routing_setting.min_samples":    "20",
			}))
			assert.Equal(t, 2, CurrentRequestPolicy().RetryTimes)
			assert.Equal(t, 2, common.RetryTimes, "the runtime global follows the same write")
			assert.Equal(t, "15", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.window_minutes"])
			assert.Equal(t, "60", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_interval_seconds"], "an old option set gets the probe defaults")
			assert.Equal(t, "false", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_enabled"])
			assert.Equal(t, "false", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_enabled"])
			var defaultDays []int
			require.NoError(t, common.UnmarshalJsonStr(CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_days"], &defaultDays))
			assert.Equal(t, []int{1, 2, 3, 4, 5, 6, 7}, defaultDays)
			assert.Equal(t, "09:00", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_start"])
			assert.Equal(t, "23:00", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_end"])
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{
				"adaptive_channel_routing_setting.probe_enabled":           "true",
				"adaptive_channel_routing_setting.probe_model":             "probe-model",
				"adaptive_channel_routing_setting.probe_interval_seconds":  "120",
				"adaptive_channel_routing_setting.probe_max_requests":      "8",
				"adaptive_channel_routing_setting.probe_max_output_tokens": "32",
			}))
			probeSnapshot := CurrentRequestPolicy()
			assert.Equal(t, "true", probeSnapshot.Options["adaptive_channel_routing_setting.probe_enabled"])
			assert.Equal(t, "probe-model", probeSnapshot.Options["adaptive_channel_routing_setting.probe_model"])
			assert.Equal(t, "120", probeSnapshot.Options["adaptive_channel_routing_setting.probe_interval_seconds"])
			assert.Equal(t, "8", probeSnapshot.Options["adaptive_channel_routing_setting.probe_max_requests"])
			assert.Equal(t, "32", probeSnapshot.Options["adaptive_channel_routing_setting.probe_max_output_tokens"])
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{
				"adaptive_channel_routing_setting.probe_schedule_enabled": "true",
				"adaptive_channel_routing_setting.probe_schedule_days":    "[1,3,7]",
				"adaptive_channel_routing_setting.probe_schedule_start":   "22:00",
				"adaptive_channel_routing_setting.probe_schedule_end":     "02:00",
			}))
			var configuredDays []int
			require.NoError(t, common.UnmarshalJsonStr(CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_days"], &configuredDays))
			assert.Equal(t, []int{1, 3, 7}, configuredDays)
			assert.Equal(t, "true", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_enabled"])
			assert.Equal(t, "22:00", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_start"])
			assert.Equal(t, "02:00", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_end"])
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, []int{1, 3, 7}, operation_setting.GetAdaptiveChannelRoutingSetting().ProbeScheduleDays)
			assert.Equal(t, "22:00", operation_setting.GetAdaptiveChannelRoutingSetting().ProbeScheduleStart)
			assert.Equal(t, "02:00", operation_setting.GetAdaptiveChannelRoutingSetting().ProbeScheduleEnd)
			for _, value := range []string{"0", "61", "bad"} {
				assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"adaptive_channel_routing_setting.window_minutes": value}))
			}
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"adaptive_channel_routing_setting.min_samples": "10001"}))
			for _, invalid := range []map[string]string{
				{"adaptive_channel_routing_setting.probe_model": ""},
				{"adaptive_channel_routing_setting.probe_model": strings.Repeat("x", 129)},
				{"adaptive_channel_routing_setting.probe_model": "bad\u0000name"},
				{"adaptive_channel_routing_setting.probe_interval_seconds": "29"},
				{"adaptive_channel_routing_setting.probe_interval_seconds": "3601"},
				{"adaptive_channel_routing_setting.probe_max_requests": "0"},
				{"adaptive_channel_routing_setting.probe_max_requests": "101"},
				{"adaptive_channel_routing_setting.probe_max_output_tokens": "0"},
				{"adaptive_channel_routing_setting.probe_max_output_tokens": "1025"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "[]"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "[0]"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "[8]"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "[1,1]"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "[1,1.5]"},
				{"adaptive_channel_routing_setting.probe_schedule_days": "null"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "9:00"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "24:00"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "09:60"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "09:0"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "09:00", "adaptive_channel_routing_setting.probe_schedule_end": "09:00"},
				{"adaptive_channel_routing_setting.probe_schedule_start": "", "adaptive_channel_routing_setting.probe_schedule_end": ""},
				{"adaptive_channel_routing_setting.probe_schedule_enabled": "false", "adaptive_channel_routing_setting.probe_schedule_start": "bad", "adaptive_channel_routing_setting.probe_schedule_end": ""},
			} {
				assert.Error(t, UpdateRequestPolicyOptions(invalid))
			}
			assert.Equal(t, "probe-model", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_model"], "a rejected probe option does not partially write")
			assert.Equal(t, "[1,3,7]", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_days"])
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{
				"adaptive_channel_routing_setting.probe_schedule_enabled": "false",
				"adaptive_channel_routing_setting.probe_schedule_start":   "",
				"adaptive_channel_routing_setting.probe_schedule_end":     "",
			}))
			assert.Equal(t, "false", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_enabled"])
			assert.Equal(t, "", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_start"])
			assert.Equal(t, "", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_schedule_end"])
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{"adaptive_channel_routing_setting.probe_model": "  probe-model  "}))
			assert.Equal(t, "probe-model", CurrentRequestPolicy().Options["adaptive_channel_routing_setting.probe_model"], "the stored string is trimmed consistently")
			var persistedProbe Option
			require.NoError(t, db.Where(map[string]any{"key": "adaptive_channel_routing_setting.probe_model"}).First(&persistedProbe).Error)
			assert.Equal(t, "probe-model", persistedProbe.Value)
			require.NoError(t, UpdateOption("AutomaticRetryStatusCodes", "429,500-503"))
			assert.Equal(t, "429,500-503", CurrentRequestPolicy().Options["AutomaticRetryStatusCodes"])
			assert.Equal(t, "429,500-503", operation_setting.AutomaticRetryStatusCodesToString())
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"request_policy_setting.mode": "off"}), "the removed mode switch is not a policy option")
			assert.False(t, CurrentRequestPolicy().Affinity.Rules[0].SkipRetryOnFailure)
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"])
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"], "legacy rule JSON survives reloads without dropping extension fields")
			require.NoError(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.session_mode": "strict"}))
			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.Equal(t, "strict", CurrentRequestPolicy().Affinity.SessionMode)
			assert.Equal(t, rules, CurrentRequestPolicy().Options["channel_affinity_setting.rules"], "a global mode never rewrites rule fields")
			snapshot := CurrentRequestPolicy()
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.session_mode": "unknown"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			for _, value := range []string{"-1", "1.5", "bad", strconv.Itoa(math.MaxInt)} {
				assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"RetryTimes": value}))
				assert.Same(t, snapshot, CurrentRequestPolicy())
			}
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"channel_affinity_setting.rules": "[", "RetryTimes": "8"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			var option Option
			require.NoError(t, db.Where(map[string]any{"key": "RetryTimes"}).First(&option).Error)
			assert.Equal(t, "2", option.Value)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("policy_fail", func(tx *gorm.DB) {
				if tx.Statement.Table == "policy_test_options" {
					tx.AddError(errors.New("save rejected"))
				}
			}))
			assert.Error(t, UpdateRequestPolicyOptions(map[string]string{"RetryTimes": "8", "channel_affinity_setting.session_mode": "off"}))
			assert.Same(t, snapshot, CurrentRequestPolicy())
			require.NoError(t, db.Callback().Update().Remove("policy_fail"))
			option = Option{}
			require.NoError(t, db.Where(map[string]any{"key": "RetryTimes"}).First(&option).Error)
			assert.Equal(t, "2", option.Value)
			loadOptionsFromDatabase()
			assert.Equal(t, "strict", CurrentRequestPolicy().Affinity.SessionMode, "a failed save keeps the persisted global mode")
		})
	}
}
