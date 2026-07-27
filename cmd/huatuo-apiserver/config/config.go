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
	"net"
	"net/url"
	"strings"

	internalconfig "huatuo-bamai/internal/config"
)

const maxAggregationIntervalSeconds = 1200

// LogConfig controls process logging.
type LogConfig struct {
	Level string
}

// ProfilingConfig controls profiler subprocess execution.
type ProfilingConfig struct {
	AggregationIntervalSeconds     int
	MaxConcurrentProfilerProcesses int
	DashboardBaseURL               string
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

// JobQuotaConfig controls active jobs for one job category.
type JobQuotaConfig struct {
	MaxConcurrentPerHost int
	MaxConcurrent        int
}

// JobsConfig controls job persistence and quotas.
type JobsConfig struct {
	Profiling JobQuotaConfig
	Tracing   JobQuotaConfig
	StoreDSN  string
}

// AgentConfig controls communication with huatuo-bamai Agents.
type AgentConfig struct {
	HTTPPort                          int
	RequestTimeoutSeconds             int
	StatusPollingIntervalSeconds      int
	MaxConsecutiveStatusPollingErrors int
}

// Config contains API server configuration.
type Config struct {
	Log           LogConfig
	Runtime       RuntimeConfig
	APIServer     APIServerConfig
	Auth          AuthConfig
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
			StoreDSN: "jobs.db",
		},
		Agent: AgentConfig{
			HTTPPort:                          19704,
			RequestTimeoutSeconds:             10,
			StatusPollingIntervalSeconds:      5,
			MaxConsecutiveStatusPollingErrors: 3,
		},
		Elasticsearch: internalconfig.ElasticsearchConfig{
			Index: "huatuo_bamai",
		},
		Profiling: ProfilingConfig{
			AggregationIntervalSeconds:     10,
			MaxConcurrentProfilerProcesses: 10,
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
	return nil
}

// Validate rejects profiling settings that cannot produce a valid job.
func (c ProfilingConfig) Validate() error {
	if c.AggregationIntervalSeconds <= 0 {
		return errors.New("aggregation interval must be greater than zero seconds")
	}
	if c.AggregationIntervalSeconds >= maxAggregationIntervalSeconds {
		return fmt.Errorf(
			"aggregation interval must be less than %d seconds",
			maxAggregationIntervalSeconds,
		)
	}
	if c.MaxConcurrentProfilerProcesses < 0 {
		return errors.New("maximum concurrent profiler processes must not be negative")
	}
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
func (c JobsConfig) Validate() error {
	if err := c.Profiling.validate("profiling"); err != nil {
		return err
	}
	if err := c.Tracing.validate("tracing"); err != nil {
		return err
	}
	if strings.TrimSpace(c.StoreDSN) == "" {
		return errors.New("store DSN is required")
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
	values := []struct {
		name  string
		value int
	}{
		{name: "http port", value: c.HTTPPort},
		{name: "request timeout", value: c.RequestTimeoutSeconds},
		{name: "status polling interval", value: c.StatusPollingIntervalSeconds},
		{name: "maximum consecutive status polling errors", value: c.MaxConsecutiveStatusPollingErrors},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
	}
	if c.HTTPPort > 65535 {
		return errors.New("http port must not exceed 65535")
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
