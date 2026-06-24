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
	"os"
	"path/filepath"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/output"
)

// writeFolded persists the folded-stack data to a timestamped .folded file.
func writeFolded(dir string, f output.Formatter) error {
	file, err := createOutputFile(dir, "perf", ".folded")
	if err != nil {
		return err
	}
	defer file.Close()

	if err := f.Write(file); err != nil {
		return fmt.Errorf("failed to write folded data: %w", err)
	}

	log.P().WithField("path", file.Name()).Infof("profiling data written")

	return nil
}

// writeFlameGraph persists the aggregated data as a flame graph SVG.
func writeFlameGraph(dir string, f output.Formatter) error {
	file, err := createOutputFile(dir, "flamegraph", ".svg")
	if err != nil {
		return err
	}
	defer file.Close()

	if err := f.Write(file); err != nil {
		return fmt.Errorf("failed to render flame graph: %w", err)
	}

	log.P().WithField("path", file.Name()).Infof("flame graph written")

	return nil
}

// createOutputFile ensures the output directory exists and creates a
// timestamped file with the given prefix and extension.
func createOutputFile(dir, prefix, ext string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	fileName := fmt.Sprintf("%s_%d%s", prefix, time.Now().Unix(), ext)
	filePath := filepath.Join(dir, fileName)

	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	return file, nil
}
