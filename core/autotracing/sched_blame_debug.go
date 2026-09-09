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
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const schedBlameRatioDebugTimeLayout = "2006-01-02T15:04:05.999999999-07:00"

var schedBlameRatioDebugHeader = []string{
	"time",
	"target_dense_id",
	"target_container_id",
	"target_container_name",
	"target_css_id",
	"sample_valid",
	"irmas_waitrate_percent",
	"current_external_contention_percent",
	"historical_p99_percent",
	"external_anomaly_k",
	"external_contention_threshold_percent",
	"history_samples",
	"ready",
	"emitted",
	"target_runtime_ns",
	"internal_contention_ns",
	"external_contention_ns",
	"throttled_time_ns",
	"estimated_total_wait_ns",
	"estimated_total_demand_ns",
}

type schedBlameExternalRatioDebugSample struct {
	targetIndex          int
	cssID                uint16
	cssKnown             bool
	containerID          string
	containerName        string
	sampleValid          bool
	currentRatio         float64
	historicalP99        float64
	externalAnomalyK     float64
	threshold            float64
	historySamples       int
	ready                bool
	targetRuntimeNs      uint64
	internalContentionNs uint64
	externalContentionNs uint64
	throttledTimeNs      uint64
	emitted              bool
	irmasSample          schedBlameIrmasSample
}

type schedBlameExternalRatioDebugBatch struct {
	timestamp time.Time
	samples   []schedBlameExternalRatioDebugSample
}

type schedBlameExternalRatioDebugWriter struct {
	file   *os.File
	buffer *bufio.Writer
	csv    *csv.Writer
}

func newSchedBlameExternalRatioDebugWriter(
	path string,
) (*schedBlameExternalRatioDebugWriter, error) {
	if path == "" {
		return nil, nil
	}
	directory := filepath.Dir(path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", directory, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	writer := &schedBlameExternalRatioDebugWriter{
		file:   file,
		buffer: bufio.NewWriterSize(file, 64*1024),
	}
	writer.csv = csv.NewWriter(writer.buffer)
	if stat.Size() == 0 {
		if err := writer.csv.Write(schedBlameRatioDebugHeader); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("write CSV header to %q: %w", path, err)
		}
		if err := writer.flush(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("flush CSV header to %q: %w", path, err)
		}
	}
	return writer, nil
}

func (writer *schedBlameExternalRatioDebugWriter) flush() error {
	writer.csv.Flush()
	if err := writer.csv.Error(); err != nil {
		return err
	}
	return writer.buffer.Flush()
}

func schedBlameOptionalPercent(value float64, available bool) string {
	if !available {
		return ""
	}
	return strconv.FormatFloat(schedBlameRatioToPercent(value), 'g', -1, 64)
}

func schedBlameIrmasDebugField(
	sample *schedBlameIrmasSample,
) string {
	if !sample.valid {
		return ""
	}
	return schedBlameOptionalPercent(sample.waitrate, true)
}

func (writer *schedBlameExternalRatioDebugWriter) writeBatch(
	batch schedBlameExternalRatioDebugBatch,
) error {
	timestamp := batch.timestamp.Format(schedBlameRatioDebugTimeLayout)
	for sampleIndex := range batch.samples {
		sample := &batch.samples[sampleIndex]
		cssID := ""
		if sample.cssKnown {
			cssID = strconv.FormatUint(uint64(sample.cssID), 10)
		}
		row := []string{
			timestamp,
			strconv.Itoa(sample.targetIndex),
			sample.containerID,
			sample.containerName,
			cssID,
			strconv.FormatBool(sample.sampleValid),
			schedBlameIrmasDebugField(
				&sample.irmasSample,
			),
			schedBlameOptionalPercent(
				sample.currentRatio,
				sample.sampleValid,
			),
			schedBlameOptionalPercent(
				sample.historicalP99,
				sample.ready,
			),
			strconv.FormatFloat(sample.externalAnomalyK, 'g', -1, 64),
			schedBlameOptionalPercent(
				sample.threshold,
				sample.ready,
			),
			strconv.Itoa(sample.historySamples),
			strconv.FormatBool(sample.ready),
			strconv.FormatBool(sample.emitted),
			strconv.FormatUint(sample.targetRuntimeNs, 10),
			strconv.FormatUint(sample.internalContentionNs, 10),
			strconv.FormatUint(sample.externalContentionNs, 10),
			strconv.FormatUint(sample.throttledTimeNs, 10),
			strconv.FormatUint(
				sample.internalContentionNs+
					sample.externalContentionNs+
					sample.throttledTimeNs,
				10,
			),
			strconv.FormatUint(
				sample.targetRuntimeNs+
					sample.internalContentionNs+
					sample.externalContentionNs+
					sample.throttledTimeNs,
				10,
			),
		}
		if err := writer.csv.Write(row); err != nil {
			return err
		}
	}
	return writer.flush()
}

func (writer *schedBlameExternalRatioDebugWriter) close() error {
	flushErr := writer.flush()
	closeErr := writer.file.Close()
	return errors.Join(flushErr, closeErr)
}

func (state *schedBlameState) newExternalRatioDebugSample(
	targetIndex int,
	runtime, internal, external, throttled uint64,
	current, historicalP99, threshold float64,
	externalAnomalyK float64,
	historySamples int,
	sampleValid, ready, emitted bool,
	irmasSample *schedBlameIrmasSample,
) schedBlameExternalRatioDebugSample {
	target := state.targets[targetIndex]
	return schedBlameExternalRatioDebugSample{
		targetIndex:          targetIndex,
		cssID:                target.cssID,
		cssKnown:             target.cssKnown,
		containerID:          target.containerID,
		containerName:        target.name,
		sampleValid:          sampleValid,
		currentRatio:         current,
		historicalP99:        historicalP99,
		externalAnomalyK:     externalAnomalyK,
		threshold:            threshold,
		historySamples:       historySamples,
		ready:                ready,
		targetRuntimeNs:      runtime,
		internalContentionNs: internal,
		externalContentionNs: external,
		throttledTimeNs:      throttled,
		emitted:              emitted,
		irmasSample:          *irmasSample,
	}
}
