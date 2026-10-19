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

package provider

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// enqueueFolded decodes a known producer format. Only async-profiler's kernel
// suffix and py-spy's exact process header grammar are interpreted as metadata.
// Java input must use collapsed,dot: without dot, allocation types can also
// carry _[k], which would incorrectly classify them as kernel frames.
func enqueueFolded(
	output profile.SampleOutput,
	labels []types.ProfileLabel,
	multiplier int64,
	language profiling.Language,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	var builder pipeline.BatchBuilder
	for line := range strings.SplitSeq(output.Output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		names, weight, err := decodeFoldedLine(line)
		if err != nil {
			return err
		}

		value, err := strconv.ParseInt(weight, 10, 64)
		if err != nil {
			return fmt.Errorf("decode folded sample weight %q: %w", weight, err)
		}

		value, err = window.ScaleValue(value, multiplier)
		if err != nil {
			return err
		}

		sampleLabels := append([]types.ProfileLabel(nil), labels...)
		for language == profiling.LanguagePython && len(names) > 0 {
			pid, entry, ok := pythonProcessHeader(names[0])
			if !ok {
				break
			}

			// py-spy emits an ancestry chain; the innermost header owns the sample.
			sampleLabels = nil
			if pid == output.PID {
				sampleLabels = append(sampleLabels, labels...)
			} else {
				sampleLabels = append(sampleLabels, types.ProfilePIDLabel(uint32(pid)))
			}

			// The source header remains authoritative even after the process exits.
			filtered := sampleLabels[:0]
			for _, label := range sampleLabels {
				if label.Name != "python_entry" {
					filtered = append(filtered, label)
				}
			}

			sampleLabels = filtered
			sampleLabels = append(sampleLabels, types.ProfileStringLabel("python_entry", entry))
			names = names[1:]
		}

		sampleLabels, err = types.NormalizeLabels(sampleLabels)
		if err != nil {
			return err
		}

		sample := aggregation.Sample{
			Labels: sampleLabels,
			Value:  value,
		}
		for _, name := range names {
			frame := types.ProfileFrame{Name: name}
			isKernel := language == profiling.LanguageJava && strings.HasSuffix(name, "_[k]")
			if isKernel {
				frame.Name = strings.TrimSuffix(name, "_[k]")
			} else if language == profiling.LanguagePython {
				frame = pythonSourceFrame(name)
			}

			// Retained frames must not keep the entire folded output alive.
			frame.Name = strings.Clone(frame.Name)
			frame.File = strings.Clone(frame.File)
			if isKernel {
				sample.KernelFrames = append(sample.KernelFrames, frame)
			} else {
				sample.UserFrames = append(sample.UserFrames, frame)
			}
		}

		if err := builder.Append(&sample, enqueue); err != nil {
			return err
		}
	}

	return builder.Flush(enqueue)
}

func decodeFoldedLine(line string) ([]string, string, error) {
	index := strings.LastIndexByte(line, ' ')
	if index < 0 {
		return nil, "", fmt.Errorf("folded sample has no weight separator")
	}

	var frames []string
	for frame := range strings.SplitSeq(line[:index], ";") {
		frame = strings.TrimSpace(frame)
		if frame != "" {
			frames = append(frames, frame)
		}
	}

	return frames, line[index+1:], nil
}

func pythonProcessHeader(name string) (int, string, bool) {
	rest, ok := strings.CutPrefix(name, "process ")
	if !ok {
		return 0, "", false
	}

	pidText, entry, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, "", false
	}

	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil || pid == 0 || strconv.FormatUint(pid, 10) != pidText {
		return 0, "", false
	}

	text, err := strconv.Unquote(entry)
	if err != nil {
		return 0, "", false
	}

	return int(pid), strings.Clone(text), true
}

// pythonSourceFrame borrows name; the sample builder owns copying retained fields.
func pythonSourceFrame(name string) types.ProfileFrame {
	frame := types.ProfileFrame{Name: name}
	index := strings.LastIndex(name, " (")
	if index < 0 || !strings.HasSuffix(name, ")") {
		return frame
	}

	position := name[index+2 : len(name)-1]
	colon := strings.LastIndexByte(position, ':')
	if colon < 0 {
		return frame
	}

	line, err := strconv.ParseInt(position[colon+1:], 10, 64)
	if err != nil || line < 0 {
		return frame
	}

	return types.ProfileFrame{
		Name: name[:index],
		File: position[:colon],
		Line: line,
	}
}
