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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

const tuiReportVersion = 1

type tuiReport struct {
	Version     int                     `json:"version"`
	ProfileType string                  `json:"profile_type"`
	Aggregated  types.AggregatedProfile `json:"aggregated"`
}

func (r *tuiReport) validateUTF8(ctx context.Context) error {
	// JSON encoding would replace invalid bytes, changing frame or label identity on replay.
	if !utf8.ValidString(r.ProfileType) {
		return fmt.Errorf("profile type contains invalid UTF-8")
	}

	for i, frame := range r.Aggregated.Frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !utf8.ValidString(frame.Name) || !utf8.ValidString(frame.File) {
			return fmt.Errorf("frame %d name or file contains invalid UTF-8", i)
		}
	}

	for i, labels := range r.Aggregated.LabelSets {
		for j := range labels {
			if err := ctx.Err(); err != nil {
				return err
			}
			label := &labels[j]
			if !utf8.ValidString(label.Name) || !utf8.ValidString(label.StringValue) || !utf8.ValidString(label.NumberUnit) {
				return fmt.Errorf("label %d in label set %d contains invalid UTF-8", j, i)
			}
		}
	}

	return ctx.Err()
}

// Report opens a saved set of profiling samples in the terminal viewer.
func Report(ctx context.Context, path string) error {
	if err := validateTerminal(); err != nil {
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

	_, err = flamegraphtui.Run(ctx, model)
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

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var report tuiReport
	if err := decoder.Decode(&report); err != nil {
		return report, err
	}
	if report.Version != tuiReportVersion {
		return report, fmt.Errorf("unsupported report version %d, expected %d", report.Version, tuiReportVersion)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return report, fmt.Errorf("unexpected data after report")
		}
		return report, err
	}
	return report, nil
}
