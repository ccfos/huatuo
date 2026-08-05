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
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIOTracingUpdateExportsLatestSnapshotWithoutReading(t *testing.T) {
	status := diskStatus{
		ReadBPS:    1.25,
		WriteBPS:   2.5,
		ReadIOPS:   3.75,
		WriteIOPS:  4.5,
		ReadAwait:  1250,
		WriteAwait: 2500,
		IOUtil:     7.25,
		QueueSize:  0.125,
	}
	var readCalls atomic.Int32
	tracer := &ioTracing{
		readSnapshot: func() (*rawDiskstatsSnapshot, error) {
			readCalls.Add(1)
			return nil, errors.New("Update must not read diskstats")
		},
	}
	tracer.latest.Store(&diskStatusSnapshot{
		devices: map[string]diskStatus{"sda": status},
		order:   []string{"sda"},
	})

	// Export rounds every gauge to two decimal places, so the published
	// values are round2 of the snapshot fields rather than the raw floats.
	wantValues := []float64{
		round2(status.ReadBPS),
		round2(status.WriteBPS),
		round2(status.ReadIOPS),
		round2(status.WriteIOPS),
		round2(status.ReadAwait / 1000),
		round2(status.WriteAwait / 1000),
		round2(status.IOUtil),
		round2(status.QueueSize),
	}
	for range 2 {
		metrics, err := tracer.Update()
		require.NoError(t, err)
		require.Len(t, metrics, len(wantValues))
		for i, want := range wantValues {
			assert.Equal(t, want, metrics[i].Value)
		}
	}
	assert.Zero(t, readCalls.Load())
}

func TestIOTracingUpdateRoundsExportedValuesToTwoDecimals(t *testing.T) {
	status := diskStatus{
		ReadBPS:    1234.567,
		WriteBPS:   2345.678,
		ReadIOPS:   3.3333,
		WriteIOPS:  4.5555,
		ReadAwait:  1250.333,
		WriteAwait: 2500.666,
		IOUtil:     94.999,
		QueueSize:  0.123,
	}
	tracer := &ioTracing{}
	tracer.latest.Store(&diskStatusSnapshot{
		devices: map[string]diskStatus{"sda": status},
		order:   []string{"sda"},
	})

	// await is stored in milliseconds and exported as seconds, so the value
	// is rounded after the /1000 conversion.
	wantValues := []float64{
		1234.57,
		2345.68,
		3.33,
		4.56,
		1.25,
		2.5,
		95,
		0.12,
	}
	metrics, err := tracer.Update()
	require.NoError(t, err)
	require.Len(t, metrics, len(wantValues))
	for i, want := range wantValues {
		assert.Equal(t, want, metrics[i].Value)
	}
}

func TestIOTracingUpdateExportsMDSnapshot(t *testing.T) {
	tracer := &ioTracing{}
	tracer.latest.Store(&diskStatusSnapshot{
		devices: map[string]diskStatus{"md0": {}},
		order:   []string{"md0"},
	})

	metrics, err := tracer.Update()
	require.NoError(t, err)
	assert.Len(t, metrics, 8)
}

func TestIOTracingRealDiskstatsMetrics(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	previous, err := readRawDiskstatsSnapshot()
	require.NoError(t, err)
	time.Sleep(time.Second)
	current, err := readRawDiskstatsSnapshot()
	require.NoError(t, err)

	snapshot := buildDiskStatusSnapshot(previous, current)
	device := strings.TrimSpace(os.Getenv("TEST_IOSTAT_DEVICE"))
	if device != "" {
		if _, ok := snapshot.devices[device]; !ok {
			t.Fatalf("TEST_IOSTAT_DEVICE %q is not a monitored whole disk", device)
		}
		snapshot = &diskStatusSnapshot{
			devices: map[string]diskStatus{device: snapshot.devices[device]},
			order:   []string{device},
		}
	}
	if len(snapshot.devices) == 0 {
		t.Skip("no supported whole disk found")
	}

	tracer := &ioTracing{}
	tracer.latest.Store(snapshot)
	metrics, err := tracer.Update()
	require.NoError(t, err)
	assert.Len(t, metrics, len(snapshot.devices)*8)
}
