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
	internalconfig "huatuo-bamai/internal/config"
)

// ComConfig global common configuration
type ComConfig struct {
	LogLevel string `default:"Info"`

	// RuntimeCgroup for huatuo resource
	RuntimeCgroup struct {
		// limit cpu
		LimitCPU int64 `default:"20"`
		// limit memory (MB)
		LimitMem int64 `default:"4096"`
	}

	// APIServer addr
	APIServer struct {
		// TCPAddr is the tcp monitoring information of the huatuo-apiserver.
		TCPAddr string `default:":12740"`
	}

	// Auth contains authentication-related configuration
	Auth struct {
		// Users contains list of user configurations
		Users []struct {
			// ID is the unique identifier for the user
			ID string
			// Name is the display name of the user
			Name string
			// Permissions defines what actions the user can perform
			Permissions []string
			// IsAdmin indicates if the user has administrator privileges
			IsAdmin bool `default:"false"`
		}
	}

	// TaskConfig contains task-related configuration
	TaskConfig struct {
		// MaxProfilingTasksPerHost is the maximum number of profiling tasks allowed per host
		MaxProfilingTasksPerHost int `default:"3"`
		// MaxTracingTasksPerHost is the maximum number of tracing tasks allowed per host
		MaxTracingTasksPerHost int `default:"5"`
		// MaxTotalProfilingTasks is the maximum number of total profiling tasks allowed
		MaxTotalProfilingTasks int `default:"500"`
		// MaxTotalTracingTasks is the maximum number of total tracing tasks allowed
		MaxTotalTracingTasks int `default:"1000"`
	}

	ElasticSearch struct {
		Debug                              bool `default:"false"`
		Address, Username, Password, Index string
	}

	Profiling struct {
		AggregationInterval  int `default:"10"`
		ExecutionTimeout     int `default:"20"`
		MaxProfilerProcesses int `default:"10"`
		// FlameGraphBaseURL is the base URL for the flame graph dashboard.
		FlameGraphBaseURL string `default:"http://localhost:8006/d"`
	}
}

var userConfig = &ComConfig{}

// Load load config file
func Load(configFile string) error {
	if err := internalconfig.Load(configFile, userConfig); err != nil {
		return err
	}
	userConfig.RuntimeCgroup.LimitMem *= 1024 * 1024
	return nil
}

// Get return the global configuration obj
func Get() *ComConfig {
	return userConfig
}
