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
	"huatuo-bamai/internal/log"
)

// LogConf holds log configuration.
type LogConf struct {
	Level string `default:"Info"`
	File  string
}

// APIServerConf holds api server configuration.
type APIServerConf struct {
	TCPAddr string `default:":19704"`
}

// RuntimeCgroupConf holds runtime cgroup configuration.
type RuntimeCgroupConf struct {
	LimitInitCPU float64 `default:"0.5"`
	LimitCPU     float64 `default:"2.0"`
	LimitMem     int64   `default:"2048"`
}

// StorageConf holds storage configuration.
type StorageConf struct {
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

// TaskConfigConf holds task related configuration.
type TaskConfigConf struct {
	MaxRunningTask int `default:"10"`
}

// PodConf holds pod configuration.
type PodConf struct {
	KubeletReadOnlyPort   uint32 `default:"10255"`
	KubeletAuthorizedPort uint32 `default:"10250"`
	KubeletClientCertPath string
	DockerAPIVersion      string `default:"1.24"`
}

// HuaTuoConfig is the global huatuo configuration.
type HuaTuoConfig struct {
	Log             LogConf
	BlackList       []string
	APIServer       APIServerConf
	RuntimeCgroup   RuntimeCgroupConf
	Storage         StorageConf
	TaskConfig      TaskConfigConf
	AutoTracing     autotracing.Config
	EventTracing    events.Config
	MetricCollector collector.Config
	Pod             PodConf
}

var (
	configFile = ""
	cfg        = &HuaTuoConfig{}

	// Region is host and containers belong to.
	Region string
)

// Load loads the config file and updates module level configs.
func Load(path string) error {
	cfg = &HuaTuoConfig{}
	if err := internalconfig.Load(path, cfg); err != nil {
		return err
	}

	cfg.RuntimeCgroup.LimitMem *= 1024 * 1024
	configFile = path
	applyModuleConfigs()

	log.Infof("Loadconfig:\n%+v\n", cfg)
	return nil
}

// Get returns the global configuration.
func Get() *HuaTuoConfig {
	return cfg
}

// Set updates a config field by dot-separated key.
func Set(key string, val any) {
	internalconfig.Set(cfg, canonicalKey(key), val)
	applyModuleConfigs()
	log.Infof("Config: set %s = %v", key, val)
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

func applyModuleConfigs() {
	autotracing.SetConfig(&cfg.AutoTracing)
	events.SetConfig(&cfg.EventTracing)
	collector.SetConfig(&cfg.MetricCollector)
}
