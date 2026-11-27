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

package tui

import (
	"bufio"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	tuiGobVersion = 2
	tuiGobMagic   = "HUATUO TUI GOB\n"
)

type tuiProfile struct {
	Version        int
	ProfileType    string
	Aggregated     types.AggregatedProfile
	DroppedSamples map[string]uint64
}

func writeTUIGob(ctx context.Context, writer io.Writer, profile *tuiProfile) error {
	w := contextWriter{ctx: ctx, writer: writer}
	if _, err := io.WriteString(w, tuiGobMagic); err != nil {
		return fmt.Errorf("write report header: %w", err)
	}
	if err := gob.NewEncoder(w).Encode(profile); err != nil {
		return fmt.Errorf("encode gob report: %w", err)
	}
	return nil
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	var written int
	for written < len(p) {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}

		end := written + min(len(p)-written, 64*1024)
		n, err := w.writer.Write(p[written:end])
		written += n
		if err != nil {
			return written, err
		}
		if written != end {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

// RunTerminalViewer loads a saved report and runs the viewer until it exits.
func RunTerminalViewer(ctx context.Context, path string) error {
	if err := output.ValidateTUITerminal(); err != nil {
		return err
	}
	decoded, err := readTUIGob(ctx, path)
	if err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}

	metadata, err := profilewindow.Metadata(decoded.ProfileType)
	if err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}
	if err := profilewindow.ValidateAggregated(ctx, &decoded.Aggregated); err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}
	model, err := buildViewerModel(ctx, &decoded, metadata[2])
	if err != nil {
		return fmt.Errorf("prepare report %q: %w", path, err)
	}
	if model == nil {
		return fmt.Errorf("report %q has no displayable samples", path)
	}

	return flamegraphtui.Run(ctx, model, flamegraphtui.ViewerManagedSignals)
}

func readTUIGob(ctx context.Context, path string) (_ tuiProfile, returnErr error) {
	if err := ctx.Err(); err != nil {
		return tuiProfile{}, err
	}

	// Opening a FIFO without O_NONBLOCK could wait before the file type check.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return tuiProfile{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()

	info, err := file.Stat()
	if err != nil {
		return tuiProfile{}, err
	}
	if !info.Mode().IsRegular() {
		return tuiProfile{}, fmt.Errorf("report must be a regular file")
	}

	return decodeTUIGob(ctx, file)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	// Bound reads so a large gob buffer cannot delay the next cancellation check.
	return r.reader.Read(p[:min(len(p), 64*1024)])
}

func decodeTUIGob(ctx context.Context, source io.Reader) (tuiProfile, error) {
	reader := bufio.NewReader(contextReader{ctx: ctx, reader: source})
	var header [len(tuiGobMagic)]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return tuiProfile{}, fmt.Errorf("read gob report header: %w", err)
	}
	if string(header[:]) != tuiGobMagic {
		return tuiProfile{}, fmt.Errorf("unsupported report format, expected a HuaTuo gob report")
	}

	var profile tuiProfile
	// The gob decoder shares this buffered reader, so trailing bytes remain visible.
	if err := gob.NewDecoder(reader).Decode(&profile); err != nil {
		return tuiProfile{}, fmt.Errorf("decode gob report: %w", err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return tuiProfile{}, fmt.Errorf("unexpected data after gob report")
		}
		return tuiProfile{}, fmt.Errorf("check end of gob report: %w", err)
	}

	if profile.Version != 1 && profile.Version != tuiGobVersion {
		return tuiProfile{}, fmt.Errorf("unsupported report version %d, expected %d", profile.Version, tuiGobVersion)
	}
	if profile.Version == tuiGobVersion {
		if err := profilewindow.ValidateDropReasons(profile.DroppedSamples); err != nil {
			return tuiProfile{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return tuiProfile{}, err
	}
	return profile, nil
}
