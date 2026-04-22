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
	"strings"

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/core/events"
	collector "huatuo-bamai/core/metrics"
	internalconfig "huatuo-bamai/internal/config"
)

// Log holds log configuration.
type Log struct {
	Level string `default:"Info"`
	File  string
}

// APIServer holds api server configuration.
type APIServer struct {
	TCPAddr string `default:":19704"`
}

// RuntimeCgroup holds runtime cgroup configuration.
type RuntimeCgroup struct {
	LimitInitCPU float64 `default:"0.5"`
	LimitCPU     float64 `default:"2.0"`
	LimitMem     int64   `default:"2048"`
}

// Storage holds storage configuration.
type Storage struct {
	ES struct {
		Address            string `default:"http://127.0.0.1:9200"`
		Username, Password string
		Index              string `default:"huatuo_bamai"`
	}

	LocalFile struct {
		Path         string `default:"huatuo-local"`
		RotationSize int    `default:"100"`
		MaxRotation  int    `default:"10"`
	}
}

// Task holds task related configuration.
type Task struct {
	MaxRunningTask int `default:"10"`
}

// Pod holds pod configuration.
type Pod struct {
	KubeletReadOnlyPort   uint32 `default:"10255"`
	KubeletAuthorizedPort uint32 `default:"10250"`
	KubeletClientCertPath string
	DockerAPIVersion      string `default:"1.24"`
}

// BamaiConfig is the global huatuo-bamai configuration.
type BamaiConfig struct {
	Log             Log
	BlackList       []string
	APIServer       APIServer
	RuntimeCgroup   RuntimeCgroup
	Storage         Storage
	TaskConfig      Task
	AutoTracing     autotracing.Config
	EventTracing    events.Config
	MetricCollector collector.Config
	Pod             Pod
}

var (
	configFile = ""
	cfg        = &BamaiConfig{}
	Region     string
)

// Load loads the config file and updates module level configs.
func Load(path string) error {
	cfg = &BamaiConfig{}
	if err := internalconfig.Load(path, cfg); err != nil {
		return err
	}

	cfg.RuntimeCgroup.LimitMem *= 1024 * 1024
	configFile = path
	setCoreModuleConfig()
	return nil
}

// Get returns the bamai configuration.
func Get() *BamaiConfig {
	return cfg
}

// Set updates a config field by dot-separated key.
func Set(key string, val any) {
	internalconfig.Set(cfg, canonicalKey(key), val)
	setCoreModuleConfig()
}

// Sync writes the config back to the current config file.
func Sync() error {
	return internalconfig.Sync(configFile, cfg)
}

func canonicalKey(key string) string {
	switch {
	case key == "Blacklist":
		return "BlackList"
	case strings.HasPrefix(key, "Blacklist."):
		return "BlackList." + strings.TrimPrefix(key, "Blacklist.")
	default:
		return key
	}
}

func setCoreModuleConfig() {
	autotracing.Set(&cfg.AutoTracing)
	events.Set(&cfg.EventTracing)
	collector.Set(&cfg.MetricCollector)
}
