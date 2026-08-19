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
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/core/events"
	collector "huatuo-bamai/core/metrics"
	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/matcher"
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

// Config is the global huatuo-bamai configuration.
type Config struct {
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
	// ErrInvalidUpdate identifies a config update rejected before publication.
	ErrInvalidUpdate = errors.New("config: invalid update")

	configState = struct {
		writerMu sync.Mutex
		current  atomic.Pointer[Config]
		path     string
	}{}

	Region string
)

func init() {
	configState.current.Store(&Config{})
}

// Load loads the config file and updates module level configs.
func Load(path string) error {
	loaded := &Config{}
	if err := internalconfig.Load(path, loaded); err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := loaded.Validate(); err != nil {
		return err
	}

	configState.writerMu.Lock()
	defer configState.writerMu.Unlock()

	configState.path = path
	publishConfig(loaded.Clone())
	return nil
}

// Validate rejects invalid operational settings before startup side effects.
func (c *Config) Validate() error {
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
	if err := matcher.ValidateClassifications(c.AutoTracing.IssuesList); err != nil {
		return fmt.Errorf("validating autotracing issues list: %w", err)
	}
	if err := c.EventTracing.Validate(); err != nil {
		return fmt.Errorf("validating event tracing config: %w", err)
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

// Get returns the current immutable bamai configuration snapshot. Callers must
// not modify the returned value or any nested reference.
func Get() *Config {
	return configState.current.Load()
}

// Update atomically updates runtime configuration without persisting it.
func Update(values map[string]any) error {
	return update(values, false)
}

// UpdateAndSync atomically updates runtime and persisted configuration.
func UpdateAndSync(values map[string]any) error {
	return update(values, true)
}

func update(values map[string]any, persist bool) error {
	configState.writerMu.Lock()
	defer configState.writerMu.Unlock()

	next := configState.current.Load().Clone()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	if err := rejectOverlappingKeys(keys); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
	}
	for _, key := range keys {
		if err := internalconfig.Set(next, key, values[key]); err != nil {
			return fmt.Errorf("%w: setting %q: %w", ErrInvalidUpdate, key, err)
		}
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
	}

	// Detach values supplied by the caller before publishing the snapshot.
	next = next.Clone()
	if persist {
		if configState.path == "" {
			return errors.New("config path is not initialized")
		}
		if err := internalconfig.Sync(configState.path, next); err != nil {
			return fmt.Errorf("persisting config: %w", err)
		}
	}

	publishConfig(next)
	return nil
}

func rejectOverlappingKeys(keys []string) error {
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if strings.HasPrefix(keys[j], keys[i]+".") {
				return fmt.Errorf("config fields %q and %q overlap", keys[i], keys[j])
			}
		}
	}
	return nil
}

func publishConfig(next *Config) {
	autotracing.Set(&next.AutoTracing)
	events.Set(&next.EventTracing)
	collector.Set(&next.MetricCollector)
	configState.current.Store(next)
}

// Clone returns a deep copy suitable for immutable publication.
func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}

	dst := *c
	dst.BlackList = slices.Clone(c.BlackList)
	dst.AutoTracing = *c.AutoTracing.Clone()
	dst.EventTracing = *c.EventTracing.Clone()
	dst.MetricCollector = *c.MetricCollector.Clone()
	return &dst
}
