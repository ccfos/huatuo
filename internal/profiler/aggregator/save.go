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

package aggregator

import (
	"context"
	"time"

	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/pkg/types"
)

func (p *Pipeline) prepareFormatter() error {
	if p.formatter != nil {
		return nil
	}
	formatter, err := output.NewFormatter(p.pctx.OutputFormat, p.pctx.OutputWriter, p.pctx.ToolstreamClient, func(renderer output.Renderer) error {
		if p.pctx.OutputFormat.IsFlameGraph() {
			return writeFlameGraph(p.pctx.OutputPath, renderer)
		}
		return writeFolded(p.pctx.OutputPath, renderer)
	})
	if err != nil {
		return err
	}
	p.formatter = formatter
	return nil
}

func (p *Pipeline) exportWindow(window *types.ProfilingWindow) error {
	if err := p.prepareFormatter(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.pctx.Ctx), 30*time.Second)
	defer cancel()
	return p.formatter.Add(ctx, window)
}
