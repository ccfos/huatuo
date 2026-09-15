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

//go:build integration

package autotracing

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ibpf "github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	schedBlameIntegrationPerfEventHeaderSize = 8
	schedBlameIntegrationPerfLostRecordSize  = 24
)

type schedBlameIntegrationRawReader struct {
	reads     chan schedBlameIntegrationRawRead
	flushOnce sync.Once
}

type schedBlameIntegrationRawRead struct {
	record ibpf.PerfEventRawRecord
	err    error
}

func newSchedBlameIntegrationRawReader() *schedBlameIntegrationRawReader {
	return &schedBlameIntegrationRawReader{
		reads: make(chan schedBlameIntegrationRawRead, 16),
	}
}

func (reader *schedBlameIntegrationRawReader) ReadRawInto(
	record *ibpf.PerfEventRawRecord,
) error {
	read := <-reader.reads
	if read.err != nil {
		return read.err
	}
	*record = read.record
	return nil
}

func (reader *schedBlameIntegrationRawReader) Flush() error {
	reader.flushOnce.Do(func() {
		// The sentinel follows every record pending when Flush was called.
		reader.reads <- schedBlameIntegrationRawRead{
			err: ibpf.ErrPerfEventReaderFlushed,
		}
	})
	return nil
}

func (reader *schedBlameIntegrationRawReader) PerCPUBufferSize() int {
	return 4096
}

func (reader *schedBlameIntegrationRawReader) Close() error {
	return reader.Flush()
}

func (reader *schedBlameIntegrationRawReader) pushSample(raw []byte) {
	reader.reads <- schedBlameIntegrationRawRead{
		record: ibpf.PerfEventRawRecord{
			CPU:       0,
			RawSample: raw,
			PerfRecordSize: len(raw) +
				schedBlameIntegrationPerfEventHeaderSize +
				schedBlamePerfRawSizeFieldSize,
		},
	}
}

func (reader *schedBlameIntegrationRawReader) pushLost(count uint64) {
	reader.reads <- schedBlameIntegrationRawRead{
		record: ibpf.PerfEventRawRecord{
			CPU:            0,
			LostSamples:    count,
			PerfRecordSize: schedBlameIntegrationPerfLostRecordSize,
		},
	}
}

func waitForSchedBlameQueueDepth(
	t *testing.T,
	reader *schedBlamePerfReader,
	want int,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(reader.records) >= want
	}, time.Second, time.Millisecond)
}

func TestSchedBlamePipelineIntegration(t *testing.T) {
	const (
		targetCgid            = 0x100
		competitorCgid        = 0x200
		targetCSSID           = 65
		competitorCSSID       = 1057
		competitorContainerID = "fedcba9876543210"
		competitorName        = "competitor-workload"
	)

	rawReader := newSchedBlameIntegrationRawReader()
	reader := newSchedBlamePerfReader(context.Background(), rawReader, 16)
	t.Cleanup(func() {
		require.NoError(t, reader.Close())
		select {
		case <-reader.Done():
		case <-time.After(time.Second):
			t.Error("sched-blame perf reader did not stop")
		}
	})

	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[schedBlameHighlightIndex] = schedBlameTarget{
		cgid:        targetCgid,
		containerID: "0123456789abcdef",
		name:        "target-workload",
		cgroupPath:  "/target",
	}
	state.setTargets(&targets)

	ratioPath := filepath.Join(t.TempDir(), "ratios.csv")
	ratioWriter, err := newSchedBlameExternalRatioDebugWriter(ratioPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ratioWriter.close())
	})

	written := make(chan *tracing.WriteRequest, 1)
	uploader := newSchedBlameUploader(func(
		_ context.Context,
		request *tracing.WriteRequest,
	) error {
		written <- request
		return nil
	})
	t.Cleanup(uploader.close)

	runner := &schedBlameRunner{
		config: schedBlameRuntimeConfig{
			externalAnomalyK: 2,
			sliceBatchSize:   schedBlameMaxBatchSlices,
		},
		reader:              reader,
		state:               state,
		uploader:            uploader,
		ratioDebugWriter:    ratioWriter,
		superMonitorSampler: newSchedBlameSuperMonitorSampler(),
		resolveLiveContainers: func() (
			map[uint64]string,
			map[string]*pod.Container,
		) {
			return map[uint64]string{
					competitorCgid: competitorContainerID,
				}, map[string]*pod.Container{
					competitorContainerID: {
						ID:       competitorContainerID,
						Hostname: competitorName,
					},
				}
		},
	}

	rawReader.pushSample(encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	}))
	rawReader.pushSample(encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: competitorCSSID,
		Cgid:  competitorCgid,
	}))
	waitForSchedBlameQueueDepth(t, reader, 2)
	require.NoError(t, runner.drain())
	require.True(t, state.targets[schedBlameHighlightIndex].cssKnown)
	assert.Equal(t, uint16(targetCSSID),
		state.targets[schedBlameHighlightIndex].cssID)

	start := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	for sample := range schedBlameExternalMinimumSamples {
		rawReader.pushSample(encodeSchedBlameBatchForTest(
			[]schedBlamePackedSlice{
				packSchedBlameSliceForTest(
					100,
					1<<schedBlameHighlightIndex,
					competitorCSSID,
				),
				packSchedBlameSliceForTest(900, 0, targetCSSID),
			},
			0,
		))
		waitForSchedBlameQueueDepth(t, reader, 1)
		require.NoError(t, runner.drain())
		require.NoError(t, runner.evaluate(start.Add(
			time.Duration(sample)*time.Second,
		)))
	}

	rawReader.pushLost(7)
	anomalyRaw := encodeSchedBlameBatchForTest(
		[]schedBlamePackedSlice{
			packSchedBlameSliceForTest(
				600,
				1<<schedBlameHighlightIndex,
				competitorCSSID,
			),
			packSchedBlameSliceForTest(
				100,
				1<<schedBlameHighlightIndex,
				targetCSSID,
			),
			packSchedBlameSliceForTest(200, 0, targetCSSID),
		},
		0,
	)
	rawReader.pushSample(anomalyRaw)
	rawReader.pushSample(encodeSchedBlameThrottleForTest(schedBlameThrottle{
		Magic:      schedBlameThrottleMagic,
		DenseID:    schedBlameHighlightIndex,
		DurationNs: 100,
	}))
	waitForSchedBlameQueueDepth(t, reader, 2)
	require.Eventually(t, func() bool {
		return reader.LostSamples() == 7
	}, time.Second, time.Millisecond)
	require.NoError(t, runner.drain())
	require.NoError(t, runner.evaluate(start.Add(
		schedBlameExternalMinimumSamples*time.Second,
	)))

	var request *tracing.WriteRequest
	select {
	case request = <-written:
	case <-time.After(time.Second):
		t.Fatal("sched-blame anomaly was not written")
	}
	require.NotNil(t, request)
	assert.Equal(t, schedBlameExternalTracerName, request.TracerName)
	assert.Equal(t, targets[schedBlameHighlightIndex].containerID,
		request.ContainerID)
	assert.Equal(t, start.Add(
		schedBlameExternalMinimumSamples*time.Second,
	), request.ObservedTimestamp)

	payload, ok := request.TracerData.(*SchedBlameExternalData)
	require.True(t, ok)
	assert.InDelta(t, 600.0/1100.0*100.0,
		payload.CurrentExternalContentionPercent, 1e-9)
	assert.InDelta(t, 10.0, payload.HistoricalP99Percent, 1e-9)
	assert.Equal(t, 2.0, payload.ExternalAnomalyK)
	assert.InDelta(t, 20.0,
		payload.ExternalContentionThresholdPercent, 1e-9)
	assert.Equal(t, uint64(300), payload.TargetRuntimeNs)
	assert.Equal(t, uint64(100), payload.InternalContentionNs)
	assert.Equal(t, uint64(600), payload.ExternalContentionNs)
	assert.Equal(t, uint64(100), payload.ThrottledTimeNs)
	assert.Equal(t, uint64(800), payload.EstimatedTotalWaitNs)
	assert.Equal(t, uint64(1100), payload.EstimatedTotalDemandNs)
	require.Len(t, payload.TopExternalCompetitors, 1)
	assert.Equal(t, schedBlameShortID(competitorContainerID),
		payload.TopExternalCompetitors[0].ContainerID)
	assert.Equal(t, competitorName,
		payload.TopExternalCompetitors[0].ContainerName)
	assert.Equal(t, uint64(600),
		payload.TopExternalCompetitors[0].ChargeNs)

	assert.Equal(t, uint64(7), reader.LostSamples())
	assert.Equal(t, uint64(schedBlameExternalMinimumSamples+1),
		reader.deliveredSliceBatchRecords())
	assert.Equal(t, uint64(2*schedBlameExternalMinimumSamples+3),
		reader.deliveredSlices())
	pressure := reader.takePressureStats()
	assert.True(t, pressure.perfSupported)
	assert.Equal(t, uint64(len(anomalyRaw)+
		schedBlameIntegrationPerfEventHeaderSize+
		schedBlamePerfRawSizeFieldSize), pressure.perfPeakBytes)
	assert.Equal(t, uint64(4096), pressure.perfCapacityBytes)
	assert.Zero(t, state.runtimeNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.internalContentionNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.externalContentionNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.throttledTimeNsByTarget[schedBlameHighlightIndex])

	require.NoError(t, ratioWriter.flush())
	file, err := os.Open(ratioPath)
	require.NoError(t, err)
	records, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.Len(t, records, schedBlameExternalMinimumSamples+2)
	assert.Equal(t, schedBlameRatioDebugHeader, records[0])
	last := records[len(records)-1]
	assert.Equal(t, "true", last[5])
	assert.InDelta(t, 600.0/1100.0*100.0,
		mustParseSchedBlameIntegrationFloat(t, last[7]), 1e-9)
	assert.Equal(t, "10", last[8])
	assert.Equal(t, "2", last[9])
	assert.Equal(t, "20", last[10])
	assert.Equal(t, "60", last[11])
	assert.Equal(t, "true", last[12])
	assert.Equal(t, "true", last[13])
	assert.Equal(t, "300", last[14])
	assert.Equal(t, "100", last[15])
	assert.Equal(t, "600", last[16])
	assert.Equal(t, "100", last[17])
	assert.Equal(t, "800", last[18])
	assert.Equal(t, "1100", last[19])

	const finalCSSID = 2048
	rawReader.pushSample(encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: finalCSSID,
		Cgid:  0x300,
	}))
	require.NoError(t, reader.Flush())
	require.NoError(t, reader.FinalDrain(state.handleRecord))
	assert.True(t, state.cssKnown[finalCSSID])
}

func mustParseSchedBlameIntegrationFloat(t *testing.T, value string) float64 {
	t.Helper()
	var parsed float64
	_, err := fmt.Sscan(value, &parsed)
	require.NoError(t, err)
	return parsed
}
