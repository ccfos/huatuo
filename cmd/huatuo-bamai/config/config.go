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
	"strings"

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/core/events"
	collector "huatuo-bamai/core/metrics"
	internalconfig "huatuo-bamai/internal/config"
)

// LogConfig controls process logging.
type LogConfig struct {
	Level string `default:"Info"`
	File  string
}

// RuntimeConfig controls process resource limits.
type RuntimeConfig struct {
	StartupCPULimitCores float64 `default:"0.5"`
	CPULimitCores        float64 `default:"2.0"`
	MemoryLimitMiB       int64   `default:"2048"`
}

// HTTPServerConfig controls the Agent HTTP server.
type HTTPServerConfig struct {
	ListenAddress                       string `default:":19704"`
	MaxEventStreamClients               int    `default:"100"`
	EventStreamKeepAliveIntervalSeconds int    `default:"30"`
}

// LocalFileConfig controls local tracing data retention.
type LocalFileConfig struct {
	Path            string `default:"huatuo-local"`
	RotationSizeMiB int    `default:"100"`
	MaxRotatedFiles int    `default:"10"`
}

// StorageConfig controls tracing data storage.
type StorageConfig struct {
	Elasticsearch internalconfig.ElasticsearchConfig
	LocalFile     LocalFileConfig
}

// TasksConfig controls locally running tracing tasks.
type TasksConfig struct {
	MaxConcurrent int `default:"10"`
}

// PodConfig controls Pod metadata discovery.
type PodConfig struct {
	KubeletReadOnlyPort   uint32 `default:"10255"`
	KubeletAuthorizedPort uint32 `default:"10250"`
	KubeletClientCertPath string
	DockerAPIVersion      string `default:"1.24"`
}

// BamaiConfig is the global huatuo-bamai configuration.
type BamaiConfig struct {
	BlackList []string

	Log        LogConfig
	Runtime    RuntimeConfig
	HTTPServer HTTPServerConfig
	Storage    StorageConfig
	Tasks      TasksConfig

	Pod PodConfig

	AutoTracing     autotracing.Config
	EventTracing    events.Config
	MetricCollector collector.Config
}

var (
	configFile = ""
	cfg        = &BamaiConfig{}
	Region     string
)

// Load loads the config file and updates module level configs.
func Load(path string) error {
	loaded := &BamaiConfig{}
	if err := internalconfig.Load(path, loaded); err != nil {
		return err
	}
	if err := loaded.Validate(); err != nil {
		return err
	}

	cfg = loaded
	configFile = path
	setCoreModuleConfig()
	return nil
}

// Validate rejects invalid operational settings before startup side effects.
func (c *BamaiConfig) Validate() error {
	if err := c.Log.Validate(); err != nil {
		return fmt.Errorf("validating log config: %w", err)
	}
	if err := c.Runtime.Validate(); err != nil {
		return fmt.Errorf("validating runtime config: %w", err)
	}
	if err := c.HTTPServer.Validate(); err != nil {
		return fmt.Errorf("validating HTTP server config: %w", err)
	}
	if err := c.Tasks.Validate(); err != nil {
		return fmt.Errorf("validating tasks config: %w", err)
	}
	if err := c.Storage.Validate(); err != nil {
		return fmt.Errorf("validating storage config: %w", err)
	}
	if err := c.Pod.Validate(); err != nil {
		return fmt.Errorf("validating pod config: %w", err)
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

// Validate rejects invalid process resource limits.
func (c RuntimeConfig) Validate() error {
	if c.StartupCPULimitCores <= 0 {
		return errors.New("startup cpu limit must be greater than zero cores")
	}
	if c.CPULimitCores <= 0 {
		return errors.New("cpu limit must be greater than zero cores")
	}
	if c.MemoryLimitMiB <= 0 {
		return errors.New("memory limit must be greater than zero MiB")
	}
	return nil
}

// Validate rejects invalid HTTP server settings.
func (c HTTPServerConfig) Validate() error {
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddress, err)
	}
	if c.MaxEventStreamClients <= 0 {
		return errors.New("maximum event stream clients must be greater than zero")
	}
	if c.EventStreamKeepAliveIntervalSeconds <= 0 {
		return errors.New("event stream keepalive interval must be greater than zero seconds")
	}
	return nil
}

// Validate rejects invalid task concurrency.
func (c TasksConfig) Validate() error {
	if c.MaxConcurrent <= 0 {
		return errors.New("maximum concurrent tasks must be greater than zero")
	}
	return nil
}

// Validate rejects invalid storage settings.
func (c *StorageConfig) Validate() error {
	if err := c.Elasticsearch.Validate(); err != nil {
		return fmt.Errorf("validating Elasticsearch config: %w", err)
	}
	if c.LocalFile.RotationSizeMiB <= 0 {
		return errors.New("local file rotation size must be greater than zero MiB")
	}
	if c.LocalFile.MaxRotatedFiles <= 0 {
		return errors.New("maximum rotated local files must be greater than zero")
	}
	return nil
}

// Validate rejects invalid kubelet ports.
func (c PodConfig) Validate() error {
	if c.KubeletReadOnlyPort > 65535 {
		return errors.New("kubelet read-only port must not exceed 65535")
	}
	if c.KubeletAuthorizedPort > 65535 {
		return errors.New("kubelet authorized port must not exceed 65535")
	}
	return nil
}

// Get returns the bamai configuration.
func Get() *BamaiConfig {
	return cfg
}

// Set updates a config field by dot-separated key.
func Set(key string, val any) error {
	if err := internalconfig.Set(cfg, key, val); err != nil {
		return err
	}
	setCoreModuleConfig()
	return nil
}

// Sync writes the config back to the current config file.
func Sync() error {
	return internalconfig.Sync(configFile, cfg)
}

func setCoreModuleConfig() {
	autotracing.Set(&cfg.AutoTracing)
	events.Set(&cfg.EventTracing)
	collector.Set(&cfg.MetricCollector)
}
