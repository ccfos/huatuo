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

package flamegraph

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
	tuiReportVersion  = 1
	tuiReportGobMagic = "HUATUO TUI GOB\n"
)

type tuiReport struct {
	Version     int
	ProfileType string
	Aggregated  types.AggregatedProfile
}

func writeTUIReport(w io.Writer, report *tuiReport) error {
	if _, err := io.WriteString(w, tuiReportGobMagic); err != nil {
		return fmt.Errorf("write report header: %w", err)
	}
	if err := gob.NewEncoder(w).Encode(report); err != nil {
		return fmt.Errorf("encode gob report: %w", err)
	}
	return nil
}

// Report opens a saved set of profiling samples in the terminal viewer.
func Report(ctx context.Context, path string) error {
	if err := output.ValidateTUITerminal(); err != nil {
		return err
	}
	report, err := readTUIReport(path)
	if err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}

	metadata, err := profilewindow.Metadata(report.ProfileType)
	if err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}
	if err := profilewindow.ValidateAggregated(&report.Aggregated); err != nil {
		return fmt.Errorf("read report %q: %w", path, err)
	}
	model, err := prepareViewer(ctx, &report.Aggregated, metadata[2])
	if err != nil {
		return fmt.Errorf("prepare report %q: %w", path, err)
	}
	if model == nil {
		return fmt.Errorf("report %q has no displayable samples", path)
	}

	_, err = flamegraphtui.Run(ctx, model, flamegraphtui.ViewerManagedSignals)
	return err
}

func readTUIReport(path string) (_ tuiReport, returnErr error) {
	// Opening a FIFO without O_NONBLOCK could wait before the file type check.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return tuiReport{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()

	info, err := file.Stat()
	if err != nil {
		return tuiReport{}, err
	}
	if !info.Mode().IsRegular() {
		return tuiReport{}, fmt.Errorf("report must be a regular file")
	}

	reader := bufio.NewReader(file)
	var header [len(tuiReportGobMagic)]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return tuiReport{}, fmt.Errorf("read gob report header: %w", err)
	}
	if string(header[:]) != tuiReportGobMagic {
		return tuiReport{}, fmt.Errorf("unsupported report format, expected a HuaTuo gob report")
	}

	var report tuiReport
	// The gob decoder shares this buffered reader, so trailing bytes remain visible.
	if err := gob.NewDecoder(reader).Decode(&report); err != nil {
		return tuiReport{}, fmt.Errorf("decode gob report: %w", err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return tuiReport{}, fmt.Errorf("unexpected data after gob report")
		}
		return tuiReport{}, fmt.Errorf("check end of gob report: %w", err)
	}

	if report.Version != tuiReportVersion {
		return tuiReport{}, fmt.Errorf("unsupported report version %d, expected %d", report.Version, tuiReportVersion)
	}
	return report, nil
}
