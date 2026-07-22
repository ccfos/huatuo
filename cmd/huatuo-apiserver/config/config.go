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

const maxAggregationInterval = 1200

// ProfilingConfig controls profiler subprocess execution.
type ProfilingConfig struct {
	AggregationInterval int `default:"10"`
	ExecutionTimeout    int `default:"20"`
	MaxProfilerProcs    int `default:"10"`
	// FlameGraphBaseURL is the base URL for the flame graph dashboard.
	FlameGraphBaseURL string `default:"http://localhost:8006/d"`
}

type RuntimeCgroupConfig struct {
	LimitCPU int64 `default:"20"`
	LimitMem int64 `default:"4096"`
}

type APIServerConfig struct {
	TCPAddr                  string `default:":12740"`
	ReadHeaderTimeoutSeconds int    `default:"10"`
	ReadTimeoutSeconds       int    `default:"30"`
	WriteTimeoutSeconds      int    `default:"60"`
	IdleTimeoutSeconds       int    `default:"120"`
	ShutdownTimeoutSeconds   int    `default:"60"`
	MaxHeaderBytes           int    `default:"1048576"`
	MaxBodyBytes             int64  `default:"4194304"`
	RateLimit                int    `default:"200"`
	RateBurst                int    `default:"200"`
}

type UserConfig struct {
	ID          string
	Name        string
	Permissions []string
	IsAdmin     bool `default:"false"`
}

type AuthConfig struct {
	Users []UserConfig
}

type TaskConfig struct {
	MaxProfilingTasksPerHost int    `default:"3"`
	MaxTracingTasksPerHost   int    `default:"5"`
	MaxTotalProfilingTasks   int    `default:"500"`
	MaxTotalTracingTasks     int    `default:"1000"`
	JobStoreDSN              string `default:"jobs.db"`
	ShutdownConcurrency      int    `default:"16"`
}

type AgentConfig struct {
	Port                      int `default:"19704"`
	RequestTimeoutSeconds     int `default:"10"`
	StatusRetryAttempts       int `default:"3"`
	StatusRetryBackoffMillis  int `default:"100"`
	StatusPollIntervalSeconds int `default:"5"`
	MaxConsecutivePollErrors  int `default:"3"`
}

type ElasticSearchConfig struct {
	Address  string
	Username string
	Password string
	Index    string
}

// Validate rejects profiling settings that cannot produce a valid job.
func (c ProfilingConfig) Validate() error {
	if c.AggregationInterval <= 0 {
		return fmt.Errorf("aggregation interval must be greater than 0 seconds")
	}
	if c.AggregationInterval >= maxAggregationInterval {
		return fmt.Errorf("aggregation interval must be less than %d seconds", maxAggregationInterval)
	}
	minimumTimeout := c.AggregationInterval * 2
	if c.ExecutionTimeout < minimumTimeout {
		return fmt.Errorf("execution timeout must be at least %d seconds", minimumTimeout)
	}
	if c.MaxProfilerProcs < 0 {
		return fmt.Errorf("max profiler procs must not be negative")
	}

	flameGraphURL, err := url.Parse(c.FlameGraphBaseURL)
	if err != nil {
		return fmt.Errorf("parsing flame graph base url: %w", err)
	}
	if flameGraphURL.Scheme != "http" && flameGraphURL.Scheme != "https" {
		return fmt.Errorf("flame graph base url must use http or https")
	}
	if flameGraphURL.Host == "" {
		return fmt.Errorf("flame graph base url must include a host")
	}

	return nil
}

// Config contains API server configuration.
type Config struct {
	LogLevel string `default:"Info"`

	// RuntimeCgroup for huatuo resource
	RuntimeCgroup RuntimeCgroupConfig

	// APIServer addr
	APIServer APIServerConfig

	// Auth contains authentication-related configuration
	Auth AuthConfig

	// TaskConfig contains task-related configuration
	TaskConfig TaskConfig

	Agent AgentConfig

	ElasticSearch ElasticSearchConfig

	Profiling ProfilingConfig
}

func (c *Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.APIServer.TCPAddr); err != nil {
		return fmt.Errorf("invalid API server TCP address %q: %w", c.APIServer.TCPAddr, err)
	}
	if err := c.APIServer.Validate(); err != nil {
		return err
	}
	if err := c.TaskConfig.Validate(); err != nil {
		return fmt.Errorf("validating task config: %w", err)
	}
	if err := c.Agent.Validate(); err != nil {
		return fmt.Errorf("validating agent config: %w", err)
	}
	if c.RuntimeCgroup.LimitCPU <= 0 || c.RuntimeCgroup.LimitMem <= 0 {
		return errors.New("runtime cgroup limits must be greater than zero")
	}
	if len(c.Auth.Users) == 0 {
		return errors.New("at least one auth user is required")
	}
	seenUsers := make(map[string]struct{}, len(c.Auth.Users))
	for i, user := range c.Auth.Users {
		if strings.TrimSpace(user.ID) == "" {
			return fmt.Errorf("auth user %d: ID is required", i)
		}
		if _, exists := seenUsers[user.ID]; exists {
			return fmt.Errorf("auth user %d: duplicate ID %q", i, user.ID)
		}
		seenUsers[user.ID] = struct{}{}
		if !user.IsAdmin && len(user.Permissions) == 0 {
			return fmt.Errorf("auth user %q: permissions are required for non-admin users", user.ID)
		}
		for _, permission := range user.Permissions {
			parts := strings.Fields(permission)
			if len(parts) == 0 || len(parts) > 2 {
				return fmt.Errorf("auth user %q: invalid permission %q", user.ID, permission)
			}
			if len(parts) == 2 && !isHTTPMethod(parts[0]) {
				return fmt.Errorf("auth user %q: invalid permission method %q", user.ID, parts[0])
			}
		}
	}
	if err := c.Profiling.Validate(); err != nil {
		return fmt.Errorf("validating profiling config: %w", err)
	}
	if err := c.ElasticSearch.Validate(); err != nil {
		return fmt.Errorf("validating Elasticsearch config: %w", err)
	}
	return nil
}

func (c AgentConfig) Validate() error {
	values := []struct {
		name  string
		value int
	}{
		{name: "port", value: c.Port},
		{name: "request timeout", value: c.RequestTimeoutSeconds},
		{name: "status retry attempts", value: c.StatusRetryAttempts},
		{name: "status retry backoff", value: c.StatusRetryBackoffMillis},
		{name: "status poll interval", value: c.StatusPollIntervalSeconds},
		{name: "max consecutive poll errors", value: c.MaxConsecutivePollErrors},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("agent %s must be greater than zero", item.name)
		}
	}
	if c.Port > 65535 {
		return errors.New("agent port must not exceed 65535")
	}
	return nil
}

func (c *APIServerConfig) Validate() error {
	values := []struct {
		name  string
		value int64
	}{
		{name: "read header timeout", value: int64(c.ReadHeaderTimeoutSeconds)},
		{name: "read timeout", value: int64(c.ReadTimeoutSeconds)},
		{name: "write timeout", value: int64(c.WriteTimeoutSeconds)},
		{name: "idle timeout", value: int64(c.IdleTimeoutSeconds)},
		{name: "shutdown timeout", value: int64(c.ShutdownTimeoutSeconds)},
		{name: "max header bytes", value: int64(c.MaxHeaderBytes)},
		{name: "max body bytes", value: c.MaxBodyBytes},
		{name: "rate limit", value: int64(c.RateLimit)},
		{name: "rate burst", value: int64(c.RateBurst)},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("API server %s must be greater than zero", item.name)
		}
	}
	return nil
}

func (c TaskConfig) Validate() error {
	limits := []struct {
		name  string
		value int
	}{
		{name: "max profiling tasks per host", value: c.MaxProfilingTasksPerHost},
		{name: "max tracing tasks per host", value: c.MaxTracingTasksPerHost},
		{name: "max total profiling tasks", value: c.MaxTotalProfilingTasks},
		{name: "max total tracing tasks", value: c.MaxTotalTracingTasks},
		{name: "shutdown concurrency", value: c.ShutdownConcurrency},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("%s must be greater than 0", limit.name)
		}
	}
	if strings.TrimSpace(c.JobStoreDSN) == "" {
		return fmt.Errorf("job store DSN is required")
	}
	return nil
}

func (c ElasticSearchConfig) Validate() error {
	if strings.TrimSpace(c.Address) == "" {
		return errors.New("address is required")
	}
	for _, address := range strings.Split(c.Address, ",") {
		parsed, err := url.Parse(strings.TrimSpace(address))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid address %q", address)
		}
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("username and password must be configured together")
	}
	return nil
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
	cfg := &Config{}
	if err := internalconfig.Load(configFile, cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.RuntimeCgroup.LimitMem *= 1024 * 1024
	return cfg, nil
}
