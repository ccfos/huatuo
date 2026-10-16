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
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

// outputArtifact describes how a non-upload format is persisted on disk.
// Collapsed stacks and stack dumps share the perf prefix but differ in
// extension, so both parts stay together to keep routing explicit.
type outputArtifact struct {
	prefix string
	ext    string
	label  string
}

// outputArtifacts maps every file-backed format to its artifact naming.
// Upload formats (remote) and reserved formats (pprof) have no entry, so
// routing them here fails loudly instead of writing misleading data.
var outputArtifacts = map[output.OutputFormat]outputArtifact{
	output.FormatCollapsed:   {prefix: "perf", ext: ".folded", label: "folded data"},
	output.FormatFlameGraph:  {prefix: "flamegraph", ext: ".svg", label: "flame graph"},
	output.FormatSVG:         {prefix: "flamegraph", ext: ".svg", label: "flame graph"},
	output.FormatSpeedscope:  {prefix: "speedscope", ext: ".json", label: "speedscope profile"},
	output.FormatChromeTrace: {prefix: "chrometrace", ext: ".json", label: "chrome trace"},
	output.FormatDump:        {prefix: "perf", ext: ".txt", label: "stack dump"},
}

// writeOutput persists the formatter output using the artifact naming that
// matches the requested format. Filenames and log messages live in one place
// so each format routes to a distinct, recognizable file.
func writeOutput(dir string, format output.OutputFormat, f output.Formatter) error {
	if format == "" {
		// The zero value is documented as FormatCollapsed, matching NewFormatter.
		format = output.FormatCollapsed
	}

	artifact, ok := outputArtifacts[format]
	if !ok {
		return fmt.Errorf("no output artifact registered for format %q", format)
	}

	file, err := createOutputFile(dir, artifact.prefix, artifact.ext)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := f.Write(file); err != nil {
		return fmt.Errorf("failed to write %s: %w", artifact.label, err)
	}

	log.WithField("path", file.Name()).Infof("%s written", artifact.label)

	return nil
}

// createOutputFile ensures the output directory exists and creates a
// timestamped file with the given prefix and extension.
func createOutputFile(dir, prefix, ext string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("failed to generate output filename: %w", err)
	}

	fileName := fmt.Sprintf("%s_%d_%x%s", prefix, time.Now().Unix(), suffix, ext)
	filePath := filepath.Join(dir, fileName)
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	return file, nil
}
