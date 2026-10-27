// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	huatuomcp "github.com/ccfos/huatuo/internal/mcp"
	"github.com/ccfos/huatuo/internal/mcp/backend"
	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
	"github.com/ccfos/huatuo/internal/mcp/backend/storage"
	mcptool "github.com/ccfos/huatuo/internal/mcp/tool"
	mcputils "github.com/ccfos/huatuo/internal/mcp/utils"
)

const requiredConfig = `
[Agent.Auth]
BearerToken = "node-secret"

[[Auth.Users]]
ID = "admin"
BearerToken = "user-secret"
Admin = true
`

func TestLoadFileDefaults(t *testing.T) {
	config := loadTestConfig(t, requiredConfig)

	if config.Log.Level != "Info" {
		t.Fatalf("Log.Level = %q, want Info", config.Log.Level)
	}
	if config.Runtime != (RuntimeConfig{CPULimitCores: 20, MemoryLimitMiB: 4096}) {
		t.Fatalf("Runtime = %+v", config.Runtime)
	}
	if config.APIServer.ListenAddress != ":12740" ||
		config.APIServer.RateLimit != (RateLimitConfig{200, 200}) {
		t.Fatalf("APIServer = %+v", config.APIServer)
	}
	if config.Jobs.Profiling != (JobQuotaConfig{3, 500}) ||
		config.Jobs.Tracing != (JobQuotaConfig{5, 1000}) {
		t.Fatalf("Jobs quotas = %+v", config.Jobs)
	}
	wantController := JobControllerConfig{
		StatusPollIntervalSeconds:         5,
		PendingTimeoutSeconds:             30,
		CompletionGracePeriodSeconds:      60,
		NodeUnavailableGracePeriodSeconds: 30,
		JobRetentionPeriodHours:           30 * 24,
	}
	if config.Jobs.Controller != wantController || config.Jobs.StoreDSN != "jobs.db" {
		t.Fatalf("Jobs lifecycle = %+v", config.Jobs)
	}
	if config.Agent.HTTPPort != 19704 || config.Agent.Auth.BearerToken != "node-secret" {
		t.Fatalf("Agent = %+v", config.Agent)
	}
	if config.Elasticsearch.Index != "huatuo_bamai" {
		t.Fatalf("Elasticsearch.Index = %q, want huatuo_bamai", config.Elasticsearch.Index)
	}
	if config.Profiling.DashboardBaseURL != "" {
		t.Fatalf("Profiling = %+v", config.Profiling)
	}
	if !reflect.DeepEqual(config.MCP, MCPConfig{
		MaxConcurrentRequests: 200,
		MaxQueryTimeRange:     30 * 24 * 60 * 60,
		MaxRegionsPerQuery:    5,
		Events: MCPEventsConfig{
			RateLimit:            300,
			MaxDocumentsPerQuery: 500,
			ToolTimeout:          60,
		},
		Metrics: MCPMetricsConfig{
			RateLimit:              300,
			MaxSeriesPerQuery:      3000,
			MaxMetricsPerDiscovery: 800,
			ToolTimeout:            60,
		},
	}) {
		t.Fatalf("MCP = %+v", config.MCP)
	}
}

func TestMCPRejectsLimitsAboveMaximums(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		mutate func(*MCPConfig)
	}{
		{"concurrency", func(c *MCPConfig) { c.MaxConcurrentRequests = huatuomcp.MaxConcurrent + 1 }},
		{"regions", func(c *MCPConfig) { c.MaxRegionsPerQuery = mcputils.MaxRegions + 1 }},
		{"documents", func(c *MCPConfig) { c.Events.MaxDocumentsPerQuery = mcpevent.MaxDocuments + 1 }},
		{"series", func(c *MCPConfig) { c.Metrics.MaxSeriesPerQuery = mcptool.MaxSeries + 1 }},
		{"discovery", func(c *MCPConfig) { c.Metrics.MaxMetricsPerDiscovery = prombackend.MaxMetrics + 1 }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			config := defaultConfig()
			mutation.mutate(&config.MCP)
			if err := config.MCP.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed") {
				t.Fatalf("limit accepted: %v", err)
			}
		})
	}
}

func TestMCPAnonymousESConnectionIsIndependent(t *testing.T) {
	input := backend.EventConfig{Storage: storage.Connection{Address: "http://anonymous:9200", Index: "events", AuthType: "none", Username: "secret-user", Password: "secret-pass"}}
	merged, err := backend.ResolveEventConfig(&input)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Storage.Username != "" || merged.Storage.Password != "" {
		t.Fatal("anonymous endpoint inherited credentials")
	}
	config := defaultConfig()
	config.MCP.Events.Regions = []MCPEventRegionConfig{{NamePattern: "cn", Address: input.Storage.Address, Index: input.Storage.Index, AuthType: input.Storage.AuthType, Username: input.Storage.Username, Password: input.Storage.Password}}
	if err := config.MCP.Validate(); err != nil {
		t.Fatal(err)
	}
	config.MCP.Events.Regions[0].AuthType = "typo"
	if err := config.MCP.Validate(); err == nil {
		t.Fatal("invalid auth mode accepted")
	}
}

func TestMCPEventsConfigurationUsesRegionalStorage(t *testing.T) {
	config := loadTestConfig(t, requiredConfig+`
[MCP.Events]
RateLimit = 0
MaxDocumentsPerQuery = 42
ToolTimeout = 25

[[MCP.Events.Regions]]
NamePattern = "default"
Address = "http://events:9200"
Index = "events"
AuthType = "none"

[[MCP.Events.Regions]]
NamePattern = "cn"
Address = "http://cn-events:9200"
Index = "cn-events"
`)
	settings := config.MCP.Events
	if settings.MaxDocumentsPerQuery != 42 || settings.RateLimit != 0 || settings.ToolTimeout != 25 || len(settings.Regions) != 2 {
		t.Fatalf("effective events=%+v", settings)
	}
	if err := config.MCP.Validate(); err != nil {
		t.Fatal(err)
	}
	config.MCP.Events.RateLimit = -1
	if err := config.MCP.Validate(); err == nil {
		t.Fatal("negative explicit rate accepted")
	}
}

func TestMCPEventConfigurationIsIndependentFromApplicationES(t *testing.T) {
	cfg := loadTestConfig(t, requiredConfig+`
[Elasticsearch]
Address = "http://application:9200"
Username = "application-user"
Password = "application-password"
Index = "application-events"
[MCP]
Enabled = true
`)
	if len(cfg.MCP.Events.Regions) != 0 {
		t.Fatalf("application connection enabled MCP event readers: %+v", cfg.MCP.Events)
	}
	for _, test := range []struct {
		name       string
		connection storage.Connection
		wantError  string
	}{
		{name: "missing MCP index", connection: storage.Connection{Address: "http://mcp:9200"}, wantError: "index is required"},
		{name: "missing MCP password", connection: storage.Connection{Address: "http://mcp:9200", Index: "mcp-events", Username: "mcp-user"}, wantError: "configured together"},
		{name: "missing MCP region connection", wantError: "storage address and index are required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyConfig := *cfg
			copyConfig.MCP.Events.Regions = []MCPEventRegionConfig{{NamePattern: "cn", Address: test.connection.Address, Index: test.connection.Index, Username: test.connection.Username, Password: test.connection.Password}}
			if err := copyConfig.Validate(); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("application settings filled incomplete MCP config: %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestMCPEventsUsesIndependentRegionalStorage(t *testing.T) {
	config := loadTestConfig(t, requiredConfig+`
[[MCP.Events.Regions]]
NamePattern = "default"
Address = "http://events:9200"
Index = "events"
AuthType = "basic"
Username = "event-reader"
Password = "event-secret"
[[MCP.Events.Regions]]
NamePattern = "cn"
Address = "http://cn-events:9200"
Index = "cn-events"
`)
	settings := config.MCP.Events
	if len(settings.Regions) != 2 || settings.Regions[0].Address != "http://events:9200" || settings.Regions[1].Address != "http://cn-events:9200" || settings.Regions[1].Index != "cn-events" {
		t.Fatalf("Storage settings = %+v", settings)
	}
	first := settings.Regions[0].ReaderConfig()
	if first.Storage.AuthType != "basic" || first.Storage.Username != "event-reader" || first.Storage.Password != "event-secret" {
		t.Fatalf("flat event credentials were not passed to the reader: %+v", first)
	}
	second := settings.Regions[1].ReaderConfig()
	if second.Storage.AuthType != "" || second.Storage.Username != "" || second.Storage.Password != "" {
		t.Fatalf("second event region inherited credentials: %+v", second)
	}
}

func TestMCPEventsRejectsDraftElasticsearchConnectionSections(t *testing.T) {
	base := requiredConfig + `
[[MCP.Events.Regions]]
NamePattern = "default"
Address = "http://events:9200"
Index = "events"
`
	for _, oldSection := range []string{
		"[MCP.ES]\nAddress = \"http://old:9200\"\n",
		"[MCP.Events.Elasticsearch]\nAddress = \"http://old:9200\"\n",
		"[[MCP.Events.Regions]]\nNamePattern = \"cn\"\n[MCP.Events.Regions.Elasticsearch]\nAddress = \"http://old:9200\"\n",
	} {
		if _, err := LoadFile(writeTestConfig(t, base+oldSection)); err == nil {
			t.Fatalf("draft section accepted or wrong error: %v", err)
		}
	}
}

func TestLoadFileCanonicalOverrides(t *testing.T) {
	config := loadTestConfig(t, `
[Jobs]
StoreDSN = "state/jobs.db"

[Jobs.Profiling]
MaxConcurrentPerHost = 2
MaxConcurrent = 100

[Jobs.Tracing]
MaxConcurrentPerHost = 4
MaxConcurrent = 200

[Jobs.Controller]
StatusPollIntervalSeconds = 7
PendingTimeoutSeconds = 40
CompletionGracePeriodSeconds = 80
NodeUnavailableGracePeriodSeconds = 50
JobRetentionPeriodHours = 48

[Agent]
HTTPPort = 29704

[Agent.Auth]
BearerToken = "node-override"

[Profiling]
DashboardBaseURL = "https://grafana.example/d"

[[Auth.Users]]
ID = "operator"
BearerToken = "operator-secret"
Permissions = ["GET /v1/profiling/**"]
`)

	if config.Jobs.Profiling != (JobQuotaConfig{2, 100}) ||
		config.Jobs.Tracing != (JobQuotaConfig{4, 200}) {
		t.Fatalf("Jobs quotas = %+v", config.Jobs)
	}
	wantController := JobControllerConfig{7, 40, 80, 50, 48}
	if config.Jobs.Controller != wantController || config.Jobs.StoreDSN != "state/jobs.db" {
		t.Fatalf("Jobs lifecycle = %+v", config.Jobs)
	}
	if config.Agent.HTTPPort != 29704 || config.Agent.Auth.BearerToken != "node-override" {
		t.Fatalf("Agent = %+v", config.Agent)
	}
	if config.Profiling.DashboardBaseURL != "https://grafana.example/d" {
		t.Fatalf("Profiling = %+v", config.Profiling)
	}
}

func TestLoadMetricsRegionsDoNotInheritPolicy(t *testing.T) {
	config := loadTestConfig(t, `
[[MCP.Metrics.Regions]]
NamePattern = "private"
Address = "http://prometheus.example:9090"
AuthType = "bearer"
AuthToken = "default-secret"
MetricPrefix = "huatuo_"

[[MCP.Metrics.Regions]]
NamePattern = "public"
Address = "http://public.example:9090"
AuthType = ""
AuthToken = ""
MetricPrefix = ""
`+requiredConfig)
	region := config.MCP.Metrics.Regions[1]
	if region.AuthToken != "" {
		t.Fatalf("AuthToken = %v, want explicit empty value", region.AuthToken)
	}
	if region.MetricPrefix != "" {
		t.Fatalf("MetricPrefix = %v, want explicit empty value", region.MetricPrefix)
	}
}

func TestLoadMCPBackendConfiguration(t *testing.T) {
	config := loadTestConfig(t, `
[MCP]
Enabled = true
MaxConcurrentRequests = 50
MaxQueryTimeRange = 0
MaxRegionsPerQuery = 4
GuideOverlayDir = "/etc/huatuo/mcp"

[MCP.Events]
RateLimit = 180
MaxDocumentsPerQuery = 400
ToolTimeout = 90

[MCP.Metrics]
RateLimit = 170
MaxSeriesPerQuery = 2000
MaxMetricsPerDiscovery = 600
ToolTimeout = 75

[[MCP.Metrics.Regions]]
NamePattern = "^cn-"
Address = "http://cn-prometheus.example:9090"
AuthType = "bearer"
AuthToken = "region-token"
MetricPrefix = "huatuo_bamai_cn"
`+requiredConfig)
	if config.MCP.MaxConcurrentRequests != 50 || config.MCP.MaxQueryTimeRange != 0 ||
		config.MCP.MaxRegionsPerQuery != 4 || config.MCP.GuideOverlayDir != "/etc/huatuo/mcp" {
		t.Fatalf("MCP query limits = %+v", config.MCP)
	}
	if config.MCP.Events.RateLimit != 180 || config.MCP.Events.MaxDocumentsPerQuery != 400 ||
		config.MCP.Events.ToolTimeout != 90 {
		t.Fatalf("MCP.Events = %+v", config.MCP.Events)
	}
	metrics := config.MCP.Metrics
	if metrics.RateLimit != 170 ||
		metrics.MaxSeriesPerQuery != 2000 || metrics.MaxMetricsPerDiscovery != 600 ||
		metrics.ToolTimeout != 75 {
		t.Fatalf("MCP.Metrics = %+v", metrics)
	}
	region := metrics.Regions[0]
	if region.AuthType != "bearer" || region.AuthToken != "region-token" {
		t.Fatalf("MCP.Metrics.Regions[0] = %+v", region)
	}
}

func TestStorageConnectionAllowsOptionalBasicAuthentication(t *testing.T) {
	tests := []struct {
		name      string
		config    storage.Connection
		wantError string
	}{
		{
			name: "anonymous",
			config: storage.Connection{
				Address: "https://elasticsearch.example:9200",
				Index:   "huatuo_bamai",
			},
		},
		{
			name: "basic authentication",
			config: storage.Connection{
				Address:  "https://elasticsearch.example:9200",
				Username: "elastic",
				Password: "secret",
				Index:    "huatuo_bamai",
			},
		},
		{
			name: "partial basic authentication",
			config: storage.Connection{
				Address:  "https://elasticsearch.example:9200",
				Username: "elastic",
				Index:    "huatuo_bamai",
			},
			wantError: "username and password must be configured together",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				if !tt.config.Enabled() {
					t.Fatal("Enabled() = false")
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantError)
			}
		})
	}

	config := validConfig()
	config.MCP.Events.Regions = []MCPEventRegionConfig{{NamePattern: "cn", Address: "https://elasticsearch.example:9200", Index: "huatuo_bamai"}}
	if err := config.Validate(); err != nil {
		t.Fatalf("Config.Validate() anonymous MCP Elasticsearch error = %v", err)
	}
}

func TestLoadFileRejectsRemovedConfiguration(t *testing.T) {
	removed := []string{
		`[MCP.Prometheus]
RateLimit = 300`,
		`[[MCP.Events.Regions]]
NamePattern = "cn"
[MCP.Events.Regions.Storage]
Address = "http://events:9200"
Index = "events"`,
		`[MCP]
UserRateLimit = 120`,
		`[MCP.Events]
BackendRateLimit = 300`,
		`[MCP.Metrics]
BackendRateLimit = 300`,
		`[[MCP.Events.Regions]]
NamePattern = "cn"
Type = "opensearch"
Address = "http://events:9200"
Index = "events"`,
		`[MCP.Events]
Type = "elasticsearch"`,
		`[MCP.Events.Storage]
Address = "http://old:9200"`,
		`[MCP.Metrics]
Address = "http://old:9090"`,
		`[MCP]
KnowledgeOverlayDir = "/tmp/mcp"`,
		`TaskConfig = { JobStoreDSN = "jobs.db" }`,
		`[Agent.StatusPolling]
IntervalSeconds = 5`,
		`[Profiling]
AggregationIntervalSeconds = 10`,
		`[MCP.ES.Flamegraph]
MaxTopFunctions = 40`,
		`[MCP.Metrics]
BearerToken = "legacy-token"`,
		`[MCP.Metrics]
TimeoutSeconds = 30`,
		`[MCP]
MaxConcurrent = 32`,
		`[MCP]
MaxQueryRange = 259200`,
		`[MCP]
MaxRegionConcurrency = 3`,
		`[MCP]
DataDir = "/tmp/mcp"`,
		`[MCP.ES]
RateLimit = 60`,
		`[MCP.ES]
MaxQuerySize = 500`,
		`[MCP.ES]
MaxRetries = 3`,
		`[MCP.ES]
MaxDuration = 60`,
		`[MCP.ES.Profiler]
MaxTopFunctions = 50`,
		`[MCP.Metrics]
TimeoutSec = 30`,
		`[MCP.Metrics]
MaxDiscover = 800`,
		`[MCP.Metrics]
SeriesBatchSize = 50`,
		`[[MCP.ES.Regions]]
Name = "^cn-"`,
		`[[MCP.Metrics.Regions]]
Name = "^cn-"`,
	}
	for _, value := range removed {
		t.Run(strings.Split(value, "\n")[0], func(t *testing.T) {
			if _, err := LoadFile(writeTestConfig(t, value+requiredConfig)); err == nil {
				t.Fatal("LoadFile() error = nil")
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "profiling quota",
			mutate: func(config *Config) {
				config.Jobs.Profiling.MaxConcurrentPerHost = 0
			},
			wantErr: "profiling jobs per host",
		},
		{
			name: "controller polling interval",
			mutate: func(config *Config) {
				config.Jobs.Controller.StatusPollIntervalSeconds = 0
			},
			wantErr: "status poll interval seconds",
		},
		{
			name: "controller retention",
			mutate: func(config *Config) {
				config.Jobs.Controller.JobRetentionPeriodHours = 0
			},
			wantErr: "Job retention period hours",
		},
		{
			name: "Agent token missing",
			mutate: func(config *Config) {
				config.Agent.Auth.BearerToken = ""
			},
			wantErr: "Agent Auth BearerToken is required",
		},
		{
			name: "Agent token whitespace",
			mutate: func(config *Config) {
				config.Agent.Auth.BearerToken = "node secret"
			},
			wantErr: "must not contain whitespace",
		},
		{
			name: "MCP concurrency",
			mutate: func(config *Config) {
				config.MCP.MaxConcurrentRequests = 0
			},
			wantErr: "maximum concurrent requests",
		},
		{
			name: "MCP duration overflow",
			mutate: func(config *Config) {
				config.MCP.Events.ToolTimeout = math.MaxInt
			},
			wantErr: "outside the supported range",
		},
		{
			name: "MCP Prometheus URL",
			mutate: func(config *Config) {
				config.MCP.Metrics.Regions = []MCPMetricsRegionConfig{{NamePattern: "cn", Address: "file:///metrics"}}
			},
			wantErr: "absolute HTTP URL",
		},
		{
			name: "MCP Prometheus region requires address",
			mutate: func(config *Config) {
				config.MCP.Metrics.Regions = []MCPMetricsRegionConfig{{NamePattern: "^cn-"}}
			},
			wantErr: "absolute HTTP URL",
		},
		{
			name: "MCP invalid ES region pattern",
			mutate: func(config *Config) {
				config.MCP.Events.Regions = []MCPEventRegionConfig{{NamePattern: "[", Address: "http://localhost:9200", Index: "profiles"}}
			},
			wantErr: "invalid region pattern",
		},
		{
			name: "dashboard scheme",
			mutate: func(config *Config) {
				config.Profiling.DashboardBaseURL = "ftp://grafana.example/d"
			},
			wantErr: "must use http or https",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			tt.mutate(&config)
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func validConfig() Config {
	config := defaultConfig()
	config.Agent.Auth.BearerToken = "node-secret"
	config.Auth.Users = []UserConfig{{
		ID:          "admin",
		BearerToken: "user-secret",
		Admin:       true,
	}}
	return config
}

func loadTestConfig(t *testing.T, contents string) *Config {
	t.Helper()
	config, err := LoadFile(writeTestConfig(t, contents))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return config
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huatuo-apiserver.conf")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
