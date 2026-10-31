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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ccfos/huatuo/core/autotracing"
	"github.com/ccfos/huatuo/internal/tracing"
)

type schedBlameEventFile struct {
	file *os.File
}

func newSchedBlameEventFile(path string) (*schedBlameEventFile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("--output-file must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open output file %q: %w", path, err)
	}
	return &schedBlameEventFile{file: file}, nil
}

func (output *schedBlameEventFile) Write(ctx context.Context, request *tracing.WriteRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, ok := request.TracerData.(*autotracing.SchedBlameExternalData)
	if !ok {
		return fmt.Errorf("unexpected sched-blame event data type %T", request.TracerData)
	}
	internalPercent := "N/A"
	if data.CurrentInternalContentionPercent != nil {
		internalPercent = fmt.Sprintf("%.4f%%", *data.CurrentInternalContentionPercent)
	}
	event := fmt.Appendf(
		nil,
		"time=%s tracer=%s target_name=%q target_id=%s target_short_id=%s interval=%ds slice_drop_percent=%d%% current_external_contention_percent=%.4f%% current_internal_contention_percent=%s historical_p99_percent=%.4f%% external_anomaly_k=%.3f external_contention_threshold_percent=%.4f%% target_runtime_ns=%d internal_contention_ns=%d external_contention_ns=%d throttled_time_ns=%d\n",
		request.ObservedTimestamp.Format(time.RFC3339Nano),
		request.TracerName, data.TargetContainerName, request.ContainerID,
		data.TargetContainerID, data.IntervalSecs, data.SliceDropPercent,
		data.CurrentExternalContentionPercent, internalPercent,
		data.HistoricalP99Percent, data.ExternalAnomalyK,
		data.ExternalContentionThresholdPercent, data.TargetRuntimeNs,
		data.InternalContentionNs, data.ExternalContentionNs, data.ThrottledTimeNs,
	)
	for _, competitor := range data.TopExternalCompetitors {
		event = fmt.Appendf(event, "  competitor_name=%q competitor_id=%s charge_ns=%d\n",
			competitor.ContainerName, competitor.ContainerID, competitor.ChargeNs)
	}
	event = append(event, '\n')
	_, err := output.file.Write(event)
	return err
}

func (output *schedBlameEventFile) Close() error {
	return output.file.Close()
}
