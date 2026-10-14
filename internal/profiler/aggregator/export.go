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
	"compress/gzip"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

func writePprof(dir string, data any) error {
	result, ok := data.(*profiler.ProfileData)
	if !ok || result == nil {
		return fmt.Errorf("invalid pprof snapshot: %T", data)
	}
	encoded, err := result.Profile.MarshalVT()
	if err != nil {
		return fmt.Errorf("marshal pprof profile: %w", err)
	}
	file, err := createOutputFile(dir, "pprof", ".pprof.gz")
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(file)
	_, writeErr := compressed.Write(encoded)
	gzipCloseErr := compressed.Close()
	fileCloseErr := file.Close()
	if err := errors.Join(writeErr, gzipCloseErr, fileCloseErr); err != nil {
		writeErr := fmt.Errorf("write pprof profile: %w", err)
		if removeErr := os.Remove(file.Name()); removeErr != nil {
			return errors.Join(writeErr, fmt.Errorf("remove incomplete pprof profile: %w", removeErr))
		}
		return writeErr
	}
	log.WithField("path", file.Name()).Info("pprof profile written")
	return nil
}

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

	log.WithField("path", file.Name()).Infof("profiling data written")

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

	log.WithField("path", file.Name()).Infof("flame graph written")

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
