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
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"huatuo-bamai/internal/procfs/blockdevice"
	"huatuo-bamai/pkg/types"

	promblockdevice "github.com/prometheus/procfs/blockdevice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIOTracingStartRetriesReadAndKeepsSamplingWhileChildRuns(t *testing.T) {
	previousConfig := cfg
	testConfig := DefaultConfig()
	testConfig.IOTracing.RbpsThreshold = 1
	testConfig.IOTracing.WbpsThreshold = 1
	testConfig.IOTracing.UtilThreshold = 90
	testConfig.IOTracing.AwaitThreshold = 100
	testConfig.IOTracing.RunTracingToolTimeout = 1
	cfg = &testConfig
	t.Cleanup(func() {
		cfg = previousConfig
	})

	startedAt := time.Unix(100, 0)
	snapshot := func(
		at time.Time,
		readIOs,
		readSectors,
		readTicks,
		ioTicks uint64,
	) *rawDiskstatsSnapshot {
		stat := blockdevice.Diskstats{
			Info: promblockdevice.Info{
				DeviceName:  "sda",
				MajorNumber: 8,
				MinorNumber: 0,
			},
			IOStats: promblockdevice.IOStats{
				ReadIOs:         readIOs,
				ReadSectors:     readSectors,
				ReadTicks:       readTicks,
				IOsTotalTicks:   ioTicks,
				WeightedIOTicks: ioTicks,
			},
		}
		return &rawDiskstatsSnapshot{
			timestamp: at,
			devices:   map[string]blockdevice.Diskstats{"sda": stat},
			order:     []string{"sda"},
		}
	}

	readFailure := errors.New("temporary diskstats failure")
	reads := []struct {
		snapshot *rawDiskstatsSnapshot
		err      error
	}{
		{snapshot: snapshot(startedAt, 100, 1000, 100, 1000)},
		{err: readFailure},
		{snapshot: snapshot(startedAt.Add(2*time.Second), 120, 1040, 120, 3000)},
		{snapshot: snapshot(startedAt.Add(3*time.Second), 130, 1060, 130, 4000)},
		{err: readFailure},
		{snapshot: snapshot(startedAt.Add(4*time.Second), 160, 1120, 160, 5000)},
		{snapshot: snapshot(startedAt.Add(5*time.Second), 200, 1200, 200, 6000)},
		{snapshot: snapshot(startedAt.Add(6*time.Second), 260, 1320, 260, 7000)},
	}

	var readIndex atomic.Int32
	readCalls := make(chan int, len(reads))
	ticks := make(chan time.Time)
	childStarted := make(chan struct{}, 2)
	releaseChild := make(chan struct{})
	var childStarts atomic.Int32

	tracer := &ioTracing{
		thresholds: ioThresholds{
			RBPSThreshold:  testConfig.IOTracing.RbpsThreshold,
			WBPSThreshold:  testConfig.IOTracing.WbpsThreshold,
			UtilThreshold:  testConfig.IOTracing.UtilThreshold,
			AwaitThreshold: testConfig.IOTracing.AwaitThreshold,
		},
		samplingIntervalSeconds: testConfig.IOTracing.Interval,
		readSnapshot: func() (*rawDiskstatsSnapshot, error) {
			index := int(readIndex.Add(1)) - 1
			if index >= len(reads) {
				return nil, errors.New("unexpected diskstats read")
			}
			readCalls <- index + 1
			return reads[index].snapshot, reads[index].err
		},
		sampleTicks: ticks,
		runChild: func(
			ctx context.Context,
			_ *reasonSnapshot,
			_ uint64,
			_ int,
			_ int,
		) error {
			childStarts.Add(1)
			childStarted <- struct{}{}
			select {
			case <-ctx.Done():
				return nil
			case <-releaseChild:
				return nil
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() {
			close(releaseChild)
		})
		cancel()
	})
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracer.Start(ctx)
	}()

	waitRead := func(want int) {
		t.Helper()
		select {
		case got := <-readCalls:
			assert.Equal(t, want, got)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for diskstats read %d", want)
		}
	}
	tickAndWait := func(wantRead int) {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(time.Second):
			t.Fatalf("timed out sending sample tick for read %d", wantRead)
		}
		waitRead(wantRead)
	}
	readIOpsIs := func(want float64) bool {
		latest := tracer.latest.Load()
		if latest == nil {
			return false
		}
		status, ok := latest.devices["sda"]
		return ok && status.ReadIOPS == want
	}

	waitRead(1)
	tickAndWait(2)
	assert.Nil(t, tracer.latest.Load())

	tickAndWait(3)
	require.Eventually(t, func() bool {
		return readIOpsIs(10)
	}, time.Second, time.Millisecond)

	tickAndWait(4)
	select {
	case <-childStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for diagnostic child")
	}
	assert.Equal(t, int32(1), childStarts.Load())

	tickAndWait(5)
	assert.True(t, readIOpsIs(10))

	tickAndWait(6)
	require.Eventually(t, func() bool {
		return readIOpsIs(30)
	}, time.Second, time.Millisecond)
	tickAndWait(7)
	require.Eventually(t, func() bool {
		return readIOpsIs(40)
	}, time.Second, time.Millisecond)
	assert.Equal(t, int32(1), childStarts.Load())

	releaseOnce.Do(func() {
		close(releaseChild)
	})
	require.Eventually(t, func() bool {
		return !tracer.childRunning.Load()
	}, time.Second, time.Millisecond)

	tickAndWait(8)
	select {
	case <-childStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for diagnostic child after the previous child exited")
	}
	assert.Equal(t, int32(2), childStarts.Load())
	cancel()

	select {
	case err := <-startDone:
		assert.ErrorIs(t, err, types.ErrExitByCancelCtx)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ioTracing.Start to stop")
	}
	assert.Equal(t, int32(2), childStarts.Load())
}

func TestIOTracingStartAppliesUpdatedIntervalImmediately(t *testing.T) {
	previousConfig := cfg
	testConfig := DefaultConfig()
	testConfig.IOTracing.Interval = 60
	testConfig.IOTracing.RbpsThreshold = 1
	testConfig.IOTracing.WbpsThreshold = 1
	testConfig.IOTracing.UtilThreshold = 90
	testConfig.IOTracing.AwaitThreshold = 100
	Set(&testConfig)
	t.Cleanup(func() {
		Set(previousConfig)
	})

	startedAt := time.Unix(100, 0)
	snapshot := func(at time.Time, readIOs uint64) *rawDiskstatsSnapshot {
		stat := blockdevice.Diskstats{
			Info: promblockdevice.Info{
				DeviceName:  "sda",
				MajorNumber: 8,
				MinorNumber: 0,
			},
			IOStats: promblockdevice.IOStats{ReadIOs: readIOs},
		}
		return &rawDiskstatsSnapshot{
			timestamp: at,
			devices:   map[string]blockdevice.Diskstats{"sda": stat},
			order:     []string{"sda"},
		}
	}

	reads := []*rawDiskstatsSnapshot{
		snapshot(startedAt, 100),
		snapshot(startedAt.Add(3*time.Minute), 280),
		snapshot(startedAt.Add(3*time.Minute+5*time.Second), 330),
	}
	var readIndex atomic.Int32
	readCalls := make(chan int, len(reads))
	tracer := &ioTracing{
		thresholds: ioThresholds{
			RBPSThreshold:  testConfig.IOTracing.RbpsThreshold,
			WBPSThreshold:  testConfig.IOTracing.WbpsThreshold,
			UtilThreshold:  testConfig.IOTracing.UtilThreshold,
			AwaitThreshold: testConfig.IOTracing.AwaitThreshold,
		},
		samplingIntervalSeconds: testConfig.IOTracing.Interval,
		readSnapshot: func() (*rawDiskstatsSnapshot, error) {
			index := int(readIndex.Add(1)) - 1
			if index >= len(reads) {
				return nil, errors.New("unexpected diskstats read")
			}
			readCalls <- index + 1
			return reads[index], nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracer.Start(ctx)
	}()

	waitRead := func(want int, timeout time.Duration) {
		t.Helper()
		select {
		case got := <-readCalls:
			assert.Equal(t, want, got)
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for diskstats read %d", want)
		}
	}
	waitRead(1, time.Second)

	updatedConfig := testConfig
	updatedConfig.IOTracing.Interval = 1
	Set(&updatedConfig)

	waitRead(2, time.Second)
	require.Eventually(t, func() bool {
		latest := tracer.latest.Load()
		return latest != nil && latest.devices["sda"].ReadIOPS == 1
	}, time.Second, time.Millisecond)

	waitRead(3, 2*time.Second)
	require.Eventually(t, func() bool {
		latest := tracer.latest.Load()
		return latest != nil && latest.devices["sda"].ReadIOPS == 10
	}, time.Second, time.Millisecond)

	cancel()
	select {
	case err := <-startDone:
		assert.ErrorIs(t, err, types.ErrExitByCancelCtx)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ioTracing.Start to stop")
	}
}
