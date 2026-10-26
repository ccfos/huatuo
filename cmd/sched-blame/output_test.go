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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/core/autotracing"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
)

func TestStandaloneEventFileAppendsCurrentPercentages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "contention.txt")
	var eventData autotracing.SchedBlameExternalData
	require.NoError(t, json.Unmarshal([]byte(`{
"target_container_id":"0123456789abc",
"target_container_name":"target workload",
"interval_secs":1,
"current_external_contention_percent":75,
"current_internal_contention_percent":50,
"historical_p99_percent":20,
"external_anomaly_k":1.2,
"external_contention_threshold_percent":24,
"target_runtime_ns":100,
"internal_contention_ns":50,
"external_contention_ns":300,
"throttled_time_ns":400,
"top_external_competitors":[{
"container_id":"fedcba9876543","container_name":"competitor","charge_ns":300
}]
}`), &eventData))
	request := &tracing.WriteRequest{
		TracerName:        "sched-blame-external",
		ContainerID:       "0123456789abcdef",
		ObservedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 10, 23, 8, 0, 1, 2, time.UTC)},
		TracerData:        &eventData,
	}
	for range 2 {
		output, err := newSchedBlameEventFile(path)
		require.NoError(t, err)
		require.NoError(t, output.Write(context.Background(), request))
		require.NoError(t, output.Close())
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	actualOutput := string(raw)
	assert.Equal(t, 2, strings.Count(actualOutput, "tracer=sched-blame-external"))
	assert.Contains(t, actualOutput, "time=2026-10-23T08:00:01.000000002Z")
	assert.Contains(t, actualOutput, "current_external_contention_percent=75.0000%")
	assert.Contains(t, actualOutput, "current_internal_contention_percent=50.0000%")
	assert.Contains(t, actualOutput, "throttled_time_ns=400")
	assert.Contains(t, actualOutput, `competitor_name="competitor" competitor_id=fedcba9876543 charge_ns=300`)
}

func TestStandaloneEventFileInternalRatioAvailability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.txt")
	output, err := newSchedBlameEventFile(path)
	require.NoError(t, err)
	eventData := &autotracing.SchedBlameExternalData{}
	request := &tracing.WriteRequest{TracerData: eventData}
	require.NoError(t, output.Write(context.Background(), request))
	zero := 0.0
	eventData.CurrentInternalContentionPercent = &zero
	require.NoError(t, output.Write(context.Background(), request))
	require.NoError(t, output.Close())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "current_internal_contention_percent=N/A")
	assert.Contains(t, string(raw), "current_internal_contention_percent=0.0000%")
}

func TestStandaloneEventFileRejectsCancelledAndInvalidWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.txt")
	output, err := newSchedBlameEventFile(path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := &tracing.WriteRequest{TracerData: &autotracing.SchedBlameExternalData{}}
	require.ErrorIs(t, output.Write(ctx, request), context.Canceled)
	request.TracerData = "invalid data"
	require.ErrorContains(t, output.Write(context.Background(), request), "unexpected sched-blame event data type")
	require.NoError(t, output.Close())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, raw)
}

func TestStandaloneEventFileRequiresPath(t *testing.T) {
	_, err := newSchedBlameEventFile("")
	require.ErrorContains(t, err, "--output-file")
}
