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

package aggregator

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	flamegraph "huatuo-bamai/internal/profiler/output/flamegraph"
)

func (p *Pipeline) mergeStackCounts(data any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	aData, ok := data.([]byte)
	if !ok {
		log.P().Errorf("aggregate data type assertion failed: %T", data)

		return
	}

	lines := strings.Split(string(aData), "\n")

	for _, line := range lines {
		idx := strings.LastIndex(line, " ")
		if idx == -1 {
			continue
		}

		stack := line[:idx]
		countStr := strings.TrimSpace(line[idx+1:])
		count, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil {
			continue
		}

		p.stackCounts[stack] += count
	}
}

func (p *Pipeline) exportFolded() error {
	p.mu.RLock()
	if len(p.stackCounts) == 0 {
		p.mu.RUnlock()

		return fmt.Errorf("no data to write")
	}

	lines := make([]string, 0, len(p.stackCounts))
	for stack, count := range p.stackCounts {
		lines = append(lines, fmt.Sprintf("%s %d", stack, count))
	}
	p.mu.RUnlock()

	aggrData := []byte(strings.Join(lines, "\n"))
	timestamp := time.Now().Unix()
	fileName := fmt.Sprintf("perf_%d.folded", timestamp)

	if err := os.MkdirAll(p.pctx.OutputPath, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	filePath := filepath.Join(p.pctx.OutputPath, fileName)
	if err := os.WriteFile(filePath, aggrData, 0o600); err != nil {
		return fmt.Errorf("failed to write profile data: %w", err)
	}

	log.P().WithField("path", filePath).Infof("profiling data written")

	return nil
}

func (p *Pipeline) exportFlameGraph() error {
	p.mu.RLock()
	if len(p.stackCounts) == 0 {
		p.mu.RUnlock()

		return fmt.Errorf("no data in stackCounts to write")
	}

	stacks, err := p.buildRenderStacks()
	p.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("failed to convert stackCounts to stacks: %w", err)
	}

	if err := os.MkdirAll(p.pctx.OutputPath, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	timestamp := time.Now().Unix()
	fileName := fmt.Sprintf("flamegraph_%d.svg", timestamp)
	filePath := filepath.Join(p.pctx.OutputPath, fileName)

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create SVG file: %w", err)
	}
	defer file.Close()

	if err := flamegraph.Render(stacks, file); err != nil {
		return fmt.Errorf("failed to render flame graph: %w", err)
	}

	log.P().WithField("path", filePath).Infof("flame graph written")

	return nil
}

func (p *Pipeline) buildRenderStacks() ([]flamegraph.Stack, error) {
	stacks := make([]flamegraph.Stack, 0, len(p.stackCounts))

	for stackStr, count := range p.stackCounts {
		stackStr = strings.ReplaceAll(stackStr, "\"", "")

		parts := strings.Split(stackStr, ";")
		if len(parts) == 0 {
			continue
		}

		names := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				part = html.EscapeString(part)
				names = append(names, part)
			}
		}

		if len(names) > 0 {
			stacks = append(stacks, flamegraph.Stack{
				Names:   names,
				Samples: count,
			})
		}
	}

	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Samples > stacks[j].Samples
	})

	return stacks, nil
}
