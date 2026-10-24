// Copyright 2025, 2026 The HuaTuo Authors
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
	"bytes"
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/cmd/profiler/provider"
	serverpprof "github.com/ccfos/huatuo/internal/httpserver/pprof"
	"github.com/ccfos/huatuo/internal/log"
	profile "github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func runAction(cliCtx *cli.Context, signalLog *bytes.Buffer) error {
	cfg, err := loadConfig(cliCtx)
	if err != nil {
		return err
	}
	implementation, ok := profiling.ImplementationFor(cfg.Language, cfg.Provider.Type)
	if !ok {
		return fmt.Errorf("no profiling implementation for language %q", cfg.Language)
	}
	meta, err := provider.New(implementation, &cfg.Provider)
	if err != nil {
		return err
	}
	ctx, cancel := newSignalContext(cliCtx.Context, signalLog)
	defer cancel()
	if implementation == profiling.ImplementationNative {
		cleanup, err := initBpfManager(cfg)
		if err != nil {
			return err
		}
		defer cleanup()
	}

	if cliCtx.Bool("enable-pprof") {
		server, err := serverpprof.StartDefault(ctx)
		if err != nil {
			return err
		}
		defer func() {
			if err := server.Close(); err != nil {
				log.Errorf("close pprof server on 127.0.0.1:6000: %v", err)
			}
		}()

		log.Info("pprof server started on 127.0.0.1:6000")
	}

	log.Infof("using profiler: %s-%s (%s)", meta.Implementation, meta.Type, meta.Description)

	return profile.Run(ctx, profile.RunConfig{
		ID:                  cfg.TaskID,
		Duration:            cfg.Provider.Duration,
		AggregationInterval: cfg.Provider.AggregationInterval,
		OutputFormat:        cfg.Output.Format,
		OutputDestination:   cfg.Output.Destination,
	}, meta)
}
