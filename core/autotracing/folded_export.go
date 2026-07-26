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

package autotracing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/pkg/tracing"
)

const foldedSnapshotTimeLayout = "20060102T150405.000000000Z"

func exportAutotracingFoldedSnapshot(
	request *tracing.WriteRequest,
	frames []flamegraph.FrameData,
) error {
	if request == nil {
		return fmt.Errorf("folded snapshot request is nil")
	}

	backend, err := cfg.Display.ResolveBackend()
	if err != nil {
		return err
	}
	directory := strings.TrimSpace(cfg.Display.FoldedStacksDir)
	if backend != DisplayBackendPyroscope || directory == "" {
		return nil
	}

	folded, err := flamegraph.FramesToFolded(frames)
	if err != nil {
		return fmt.Errorf("convert frames: %w", err)
	}
	if len(folded) == 0 {
		return fmt.Errorf("folded snapshot has no positive self samples")
	}

	filename := strings.Join([]string{
		foldedFilenamePart(request.TracerName),
		request.TracerTime.UTC().Format(foldedSnapshotTimeLayout),
		foldedFilenamePart(request.TracerID),
	}, "-") + ".folded"
	if err := writeFoldedSnapshot(directory, filename, folded); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return nil
}

func foldedFilenamePart(value string) string {
	value = strings.Map(func(current rune) rune {
		if unicode.IsLetter(current) || unicode.IsDigit(current) ||
			current == '-' || current == '_' || current == '.' {
			return current
		}
		return '_'
	}, strings.TrimSpace(value))
	value = strings.Trim(value, ".")
	if value == "" {
		return "unknown"
	}
	return value
}

func writeFoldedSnapshot(directory, filename string, data []byte) error {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create directory %s: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".huatuo-folded-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}

	target := filepath.Join(directory, filename)
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("rename temporary file: %w", err)
	}
	removeTemporary = false
	return nil
}
