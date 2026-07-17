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

	"huatuo-bamai/internal/log"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/profiling"
)

func runAction(cliCtx *cli.Context, signalLog *bytes.Buffer) error {
	typ := profiling.Type(cliCtx.String("type"))
	lang := profiling.Language(cliCtx.String("language"))

	implementation, ok := profiling.ImplementationFor(lang)
	if !ok {
		return fmt.Errorf("no profiling implementation for language %q", lang)
	}
	if implementation == profiling.ImplementationNative {
		cleanup, err := initBpfManager(cliCtx.Int("duration"))
		if err != nil {
			return err
		}
		defer cleanup()
	}

	pctx, err := pcontext.NewProfilerContext(cliCtx, signalLog)
	if err != nil {
		return err
	}
	defer pctx.Cancel()
	if pctx.ToolstreamClient != nil {
		defer pctx.ToolstreamClient.End()
	}

	if cliCtx.Bool("enable-pprof") {
		server, err := startPprofServer(pctx.Ctx, profilerPprofAddress)
		if err != nil {
			return err
		}
		defer func() {
			if err := server.Close(); err != nil {
				log.Errorf("close pprof server on %s: %v", profilerPprofAddress, err)
			}
		}()

		log.Infof("pprof server started on %s", profilerPprofAddress)
	}

	meta, err := registry.Get(implementation, typ)
	if err != nil {
		return err
	}

	log.Infof("using profiler: %s-%s (%s)", meta.Implementation, meta.Type, meta.Description)

	return registry.Profile(pctx, meta)
}
