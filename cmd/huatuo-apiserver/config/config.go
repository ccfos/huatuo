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
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	huatuomcp "github.com/ccfos/huatuo/internal/mcp"
	"github.com/ccfos/huatuo/internal/mcp/backend"
	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
	"github.com/ccfos/huatuo/internal/mcp/backend/storage"
	mcptool "github.com/ccfos/huatuo/internal/mcp/tool"
	mcputils "github.com/ccfos/huatuo/internal/mcp/utils"
)

// LogConfig controls process logging.
type LogConfig struct {
	Level string
}

// ProfilingConfig controls profiler subprocess execution.
type ProfilingConfig struct {
	DashboardBaseURL string
}

// RuntimeConfig controls resource limits for the API server process.
type RuntimeConfig struct {
	CPULimitCores  int64
	MemoryLimitMiB int64
}

// RateLimitConfig controls the process-wide HTTP token bucket.
type RateLimitConfig struct {
	RequestsPerSecond int
	Burst             int
}

// APIServerConfig controls the HTTP server.
type APIServerConfig struct {
	ListenAddress string
	RateLimit     RateLimitConfig
}

// UserConfig defines one authenticated principal.
type UserConfig struct {
	ID          string
	BearerToken string
	Permissions []string
	Admin       bool
}

// AuthConfig controls authentication and authorization.
type AuthConfig struct {
	Users []UserConfig
}

// MCPConfig controls the Model Context Protocol endpoint.
type MCPConfig struct {
	Enabled bool
	// MaxConcurrentRequests bounds in-flight MCP HTTP requests per process,
	// shared by all users, not TCP connections or the number of users.
	MaxConcurrentRequests int
	MaxQueryTimeRange     int
	MaxRegionsPerQuery    int
	GuideOverlayDir       string
	Events                MCPEventsConfig
	Metrics               MCPMetricsConfig
}

// MCPEventsConfig selects a logical event reader and its resource policy.
type MCPEventsConfig struct {
	RateLimit            int
	MaxDocumentsPerQuery int
	// ToolTimeout is the whole-call budget in seconds, including regional queries,
	// backend attempts, retry waits, and result processing; not a per-request timeout.
	ToolTimeout int
	Regions     []MCPEventRegionConfig
}

type MCPEventRegionConfig struct {
	NamePattern string
	Address     string
	Index       string
	AuthType    string
	Username    string
	Password    string
}

func (c *MCPEventRegionConfig) ReaderConfig() backend.EventConfig {
	return backend.EventConfig{Storage: storage.Connection{
		Address: c.Address, Index: c.Index, AuthType: c.AuthType,
		Username: c.Username, Password: c.Password,
	}}
}

// MCPMetricsConfig controls the time-series metrics backend exposed through MCP.
type MCPMetricsConfig struct {
	RateLimit              int
	MaxSeriesPerQuery      int
	MaxMetricsPerDiscovery int
	// ToolTimeout is the whole-call budget in seconds, including regional queries,
	// backend attempts, retry waits, and result processing; not a per-request timeout.
	ToolTimeout int
	Regions     []MCPMetricsRegionConfig
}

// MCPMetricsRegionConfig defines an independent connection for a region pattern.
type MCPMetricsRegionConfig struct {
	NamePattern  string
	Address      string
	AuthType     string
	AuthToken    string
	AuthUser     string
	AuthPass     string
	MetricPrefix string
}

// JobQuotaConfig controls active jobs for one job category.
type JobQuotaConfig struct {
	MaxConcurrentPerHost int
	MaxConcurrent        int
}

// JobsConfig controls job persistence and quotas.
type JobsConfig struct {
	Profiling  JobQuotaConfig
	Tracing    JobQuotaConfig
	Controller JobControllerConfig
	StoreDSN   string
}

// JobControllerConfig controls Apiserver-owned Job lifecycle policy.
type JobControllerConfig struct {
	StatusPollIntervalSeconds         int
	PendingTimeoutSeconds             int
	CompletionGracePeriodSeconds      int
	NodeUnavailableGracePeriodSeconds int
	JobRetentionPeriodHours           int
}

// AgentAuthConfig authenticates Apiserver to the Node API.
type AgentAuthConfig struct {
	BearerToken string
}

// AgentConfig controls communication with huatuo-bamai Agents.
type AgentConfig struct {
	HTTPPort int
	Auth     AgentAuthConfig
}

// Config contains API server configuration.
type Config struct {
	Log           LogConfig
	Runtime       RuntimeConfig
	APIServer     APIServerConfig
	Auth          AuthConfig
	MCP           MCPConfig
	Jobs          JobsConfig
	Agent         AgentConfig
	Elasticsearch internalconfig.ElasticsearchConfig
	Profiling     ProfilingConfig
}

func defaultConfig() Config {
	return Config{
		Log: LogConfig{
			Level: "Info",
		},
		Runtime: RuntimeConfig{
			CPULimitCores:  20,
			MemoryLimitMiB: 4096,
		},
		APIServer: APIServerConfig{
			ListenAddress: ":12740",
			RateLimit: RateLimitConfig{
				RequestsPerSecond: 200,
				Burst:             200,
			},
		},
		Jobs: JobsConfig{
			Profiling: JobQuotaConfig{
				MaxConcurrentPerHost: 3,
				MaxConcurrent:        500,
			},
			Tracing: JobQuotaConfig{
				MaxConcurrentPerHost: 5,
				MaxConcurrent:        1000,
			},
			Controller: JobControllerConfig{
				StatusPollIntervalSeconds:         5,
				PendingTimeoutSeconds:             30,
				CompletionGracePeriodSeconds:      60,
				NodeUnavailableGracePeriodSeconds: 30,
				JobRetentionPeriodHours:           30 * 24,
			},
			StoreDSN: "jobs.db",
		},
		Agent: AgentConfig{
			HTTPPort: 19704,
		},
		Profiling: ProfilingConfig{},
		MCP: MCPConfig{
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
		},
	}
}

// Validate rejects invalid or incomplete API server configuration.
func (c *Config) Validate() error {
	if err := c.Log.Validate(); err != nil {
		return err
	}
	if err := c.Runtime.Validate(); err != nil {
		return fmt.Errorf("validating runtime config: %w", err)
	}
	if err := c.APIServer.Validate(); err != nil {
		return fmt.Errorf("validating API server config: %w", err)
	}
	if err := c.Jobs.Validate(); err != nil {
		return fmt.Errorf("validating jobs config: %w", err)
	}
	if err := c.Agent.Validate(); err != nil {
		return fmt.Errorf("validating agent config: %w", err)
	}
	if err := c.Auth.Validate(); err != nil {
		return fmt.Errorf("validating auth config: %w", err)
	}
	if err := c.Profiling.Validate(); err != nil {
		return fmt.Errorf("validating profiling config: %w", err)
	}
	if err := c.Elasticsearch.Validate(); err != nil {
		return fmt.Errorf("validating Elasticsearch config: %w", err)
	}
	if err := c.MCP.Validate(); err != nil {
		return fmt.Errorf("validating MCP config: %w", err)
	}
	return nil
}

// Validate rejects invalid MCP resource limits.
func (c *MCPConfig) Validate() error {
	events := c.Events
	for _, bound := range []struct {
		name           string
		value, maximum int
	}{
		{"maximum concurrent requests", c.MaxConcurrentRequests, huatuomcp.MaxConcurrent},
		{"maximum regions per query", c.MaxRegionsPerQuery, mcputils.MaxRegions},
		{"event document limit", events.MaxDocumentsPerQuery, mcpevent.MaxDocuments},
		{"metrics series limit", c.Metrics.MaxSeriesPerQuery, mcptool.MaxSeries},
		{"metrics discovery limit", c.Metrics.MaxMetricsPerDiscovery, prombackend.MaxMetrics},
	} {
		if bound.value > bound.maximum {
			return fmt.Errorf("%s must not exceed %d", bound.name, bound.maximum)
		}
	}
	if c.MaxConcurrentRequests <= 0 {
		return errors.New("maximum concurrent requests must be greater than zero")
	}
	if c.MaxQueryTimeRange < 0 {
		return errors.New("maximum query range must not be negative")
	}
	if c.MaxRegionsPerQuery <= 0 {
		return errors.New("maximum regions per query must be greater than zero")
	}
	if events.RateLimit < 0 || c.Metrics.RateLimit < 0 {
		return errors.New("rate limits must not be negative")
	}
	if events.MaxDocumentsPerQuery <= 0 || events.ToolTimeout <= 0 {
		return errors.New("event document limit and Tool timeout must be positive")
	}
	if c.Metrics.MaxSeriesPerQuery <= 0 || c.Metrics.MaxMetricsPerDiscovery <= 0 ||
		c.Metrics.ToolTimeout <= 0 {
		return errors.New("metrics query limits and Tool timeout must be positive")
	}
	maximumSeconds := math.MaxInt64 / int64(time.Second)
	for _, value := range []struct {
		name    string
		seconds int
	}{
		{name: "maximum query range", seconds: c.MaxQueryTimeRange},
		{name: "event Tool timeout", seconds: events.ToolTimeout},
		{name: "metrics Tool timeout", seconds: c.Metrics.ToolTimeout},
	} {
		if int64(value.seconds) > maximumSeconds {
			return fmt.Errorf("%s is outside the supported range", value.name)
		}
	}
	metricNames := make([]string, 0, len(c.Metrics.Regions))
	for _, region := range c.Metrics.Regions {
		metricNames = append(metricNames, region.NamePattern)
		address, err := url.Parse(region.Address)
		if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" || address.User != nil {
			return fmt.Errorf("metrics region %q address must be an absolute HTTP URL without credentials", region.NamePattern)
		}
	}
	if err := validateRegionPatterns(metricNames); err != nil {
		return fmt.Errorf("metrics regions: %w", err)
	}

	eventNames := make([]string, 0, len(events.Regions))
	for _, region := range events.Regions {
		eventNames = append(eventNames, region.NamePattern)
		override := region.ReaderConfig()
		resolved, err := backend.ResolveEventConfig(&override)
		if err != nil {
			return fmt.Errorf("event region %q: %w", region.NamePattern, err)
		}
		if !backend.EventReaderConfigured(&resolved) {
			return fmt.Errorf("event region %q: storage address and index are required", region.NamePattern)
		}
	}
	if err := validateRegionPatterns(eventNames); err != nil {
		return fmt.Errorf("event regions: %w", err)
	}
	return nil
}

func validateRegionPatterns(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return errors.New("region name is required")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate region name %q", name)
		}
		seen[name] = struct{}{}
		if _, err := regexp.Compile(name); err != nil {
			return fmt.Errorf("invalid region pattern %q: %w", name, err)
		}
	}
	return nil
}

// Validate rejects profiling settings that cannot produce a valid job.
func (c ProfilingConfig) Validate() error {
	if c.DashboardBaseURL == "" {
		return nil
	}

	dashboardURL, err := url.Parse(c.DashboardBaseURL)
	if err != nil {
		return fmt.Errorf("parsing dashboard base url: %w", err)
	}
	if dashboardURL.Scheme != "http" && dashboardURL.Scheme != "https" {
		return errors.New("dashboard base url must use http or https")
	}
	if dashboardURL.Host == "" {
		return errors.New("dashboard base url must include a host")
	}
	return nil
}

// Validate rejects invalid runtime resource limits.
func (c RuntimeConfig) Validate() error {
	if c.CPULimitCores <= 0 {
		return errors.New("cpu limit must be greater than zero cores")
	}
	if c.MemoryLimitMiB <= 0 {
		return errors.New("memory limit must be greater than zero MiB")
	}
	return nil
}

// Validate rejects invalid HTTP server settings.
func (c *APIServerConfig) Validate() error {
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddress, err)
	}

	values := []struct {
		name  string
		value int
	}{
		{name: "rate limit requests per second", value: c.RateLimit.RequestsPerSecond},
		{name: "rate limit burst", value: c.RateLimit.Burst},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
	}
	return nil
}

// Validate rejects incomplete or conflicting authentication settings.
func (c AuthConfig) Validate() error {
	if len(c.Users) == 0 {
		return errors.New("at least one user is required")
	}

	seenIDs := make(map[string]struct{}, len(c.Users))
	seenTokens := make(map[string]struct{}, len(c.Users))
	for i, user := range c.Users {
		if strings.TrimSpace(user.ID) == "" {
			return fmt.Errorf("user %d: id is required", i)
		}
		if _, exists := seenIDs[user.ID]; exists {
			return fmt.Errorf("user %d: duplicate id %q", i, user.ID)
		}
		seenIDs[user.ID] = struct{}{}

		if strings.TrimSpace(user.BearerToken) == "" {
			return fmt.Errorf("user %d: bearer token is required", i)
		}
		if _, exists := seenTokens[user.BearerToken]; exists {
			return fmt.Errorf("user %d: duplicate bearer token", i)
		}
		seenTokens[user.BearerToken] = struct{}{}

		if !user.Admin && len(user.Permissions) == 0 {
			return fmt.Errorf("user %d: permissions are required for non-admin users", i)
		}
		for _, permission := range user.Permissions {
			parts := strings.Fields(permission)
			if len(parts) == 0 || len(parts) > 2 {
				return fmt.Errorf("user %d: invalid permission %q", i, permission)
			}
			if len(parts) == 2 && !isHTTPMethod(parts[0]) {
				return fmt.Errorf(
					"user %d: invalid permission method %q",
					i,
					parts[0],
				)
			}
		}
	}
	return nil
}

// Validate rejects invalid job quotas or persistence settings.
func (c *JobsConfig) Validate() error {
	if err := c.Profiling.validate("profiling"); err != nil {
		return err
	}
	if err := c.Tracing.validate("tracing"); err != nil {
		return err
	}
	if strings.TrimSpace(c.StoreDSN) == "" {
		return errors.New("store DSN is required")
	}
	if err := c.Controller.Validate(); err != nil {
		return fmt.Errorf("invalid Job Controller config: %w", err)
	}
	return nil
}

// Validate rejects invalid Job lifecycle policy.
func (c JobControllerConfig) Validate() error {
	values := []struct {
		name  string
		value int
	}{
		{name: "status poll interval seconds", value: c.StatusPollIntervalSeconds},
		{name: "pending timeout seconds", value: c.PendingTimeoutSeconds},
		{name: "completion grace period seconds", value: c.CompletionGracePeriodSeconds},
		{name: "Node unavailable grace period seconds", value: c.NodeUnavailableGracePeriodSeconds},
		{name: "Job retention period hours", value: c.JobRetentionPeriodHours},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
	}
	if int64(c.JobRetentionPeriodHours) > math.MaxInt64/int64(time.Hour) {
		return errors.New("Job retention period is outside the supported range")
	}
	return nil
}

func (c JobQuotaConfig) validate(category string) error {
	if c.MaxConcurrentPerHost <= 0 {
		return fmt.Errorf(
			"maximum concurrent %s jobs per host must be greater than zero",
			category,
		)
	}
	if c.MaxConcurrent <= 0 {
		return fmt.Errorf(
			"maximum concurrent %s jobs must be greater than zero",
			category,
		)
	}
	return nil
}

// Validate rejects invalid Agent communication settings.
func (c AgentConfig) Validate() error {
	if c.HTTPPort <= 0 {
		return errors.New("http port must be greater than zero")
	}
	if c.HTTPPort > 65535 {
		return errors.New("http port must not exceed 65535")
	}
	if strings.TrimSpace(c.Auth.BearerToken) == "" {
		return errors.New("Agent Auth BearerToken is required")
	}
	if strings.ContainsAny(c.Auth.BearerToken, " \t\r\n") {
		return errors.New("Agent Auth BearerToken must not contain whitespace")
	}
	return nil
}

// Validate rejects unsupported log levels.
func (c LogConfig) Validate() error {
	switch strings.ToLower(c.Level) {
	case "debug", "info", "warn", "error", "panic":
		return nil
	default:
		return fmt.Errorf("unsupported log level %q", c.Level)
	}
}

func isHTTPMethod(value string) bool {
	switch strings.ToUpper(value) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	default:
		return false
	}
}

// LoadFile loads and validates a fresh configuration instance.
func LoadFile(configFile string) (*Config, error) {
	cfg := defaultConfig()
	if err := internalconfig.Load(configFile, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
