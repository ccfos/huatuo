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

package main

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/core/autotracing"
)

type standaloneConfig struct {
	Log struct {
		Level string `default:"Info"`
		File  string
	}
	Pod struct {
		KubeletReadOnlyPort   uint32 `default:"10255"`
		KubeletAuthorizedPort uint32 `default:"10250"`
		KubeletClientCertPath string
		DockerAPIVersion      string `default:"1.24"`
	}
	AutoTracing autotracing.Config
}

func loadStandaloneConfig(cliCtx *cli.Context) (*standaloneConfig, error) {
	path := cliCtx.String(flagConfig)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	config := &standaloneConfig{}
	// Accept the daemon's config while decoding only the sections this tool uses.
	if err := toml.NewDecoder(file).Decode(config); err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}
	if cliCtx.IsSet(flagHighlightContainer) {
		config.AutoTracing.SchedBlame.HighlightContainer = cliCtx.String(flagHighlightContainer)
	}
	if cliCtx.IsSet(flagRatioDebugFile) {
		config.AutoTracing.SchedBlame.ExternalRatioDebugFile = cliCtx.String(flagRatioDebugFile)
	}
	if _, err := logrus.ParseLevel(config.Log.Level); err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	if config.Pod.KubeletReadOnlyPort > 65535 ||
		config.Pod.KubeletAuthorizedPort > 65535 {
		return nil, fmt.Errorf("kubelet ports must not exceed 65535")
	}
	if err := config.AutoTracing.SchedBlame.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SchedBlame config: %w", err)
	}
	return config, nil
}
