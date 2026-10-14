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

package registry

import (
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

func newFormatter(pctx *pcontext.ProfilerContext) (output.Formatter, error) {
	format, path := pctx.OutputFormat, pctx.OutputPath
	return output.NewFormatter(format, pctx.OutputWriter, pctx.ToolstreamClient, func(renderer output.Renderer) error {
		if format.IsFlameGraph() {
			return writeFlameGraph(path, renderer)
		}
		return writeFolded(path, renderer)
	})
}
