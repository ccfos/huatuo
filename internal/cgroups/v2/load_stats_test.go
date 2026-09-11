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

package v2

import (
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

type fakeTaskLoadSnapshotter struct {
	calls  [][]uint64
	result map[uint64]stats.LoadStats
}

func (s *fakeTaskLoadSnapshotter) Snapshot(
	ids []uint64,
) (map[uint64]stats.LoadStats, error) {
	s.calls = append(s.calls, append([]uint64(nil), ids...))
	return s.result, nil
}

func TestSharedTaskLoadSnapshotterConsumers(t *testing.T) {
	collector := &fakeTaskLoadSnapshotter{result: map[uint64]stats.LoadStats{10: {NrRunning: 2}}}
	now := time.Now()
	shared := &sharedTaskLoadSnapshotter{snapshotter: collector, now: func() time.Time { return now }}
	for _, consumer := range []LoadStatsConsumer{LoadStatsConsumerLoadavg, LoadStatsConsumerDload} {
		got, err := shared.Snapshot(consumer, []uint64{10})
		if err != nil || got[10].NrRunning != 2 {
			t.Fatalf("snapshot=%v err=%v", got, err)
		}
	}
	if len(collector.calls) != 1 {
		t.Fatal("adjacent consumers did not share a scan")
	}
	now = now.Add(sharedLoadSnapshotMaxAge)
	if _, err := shared.Snapshot(LoadStatsConsumerLoadavg, []uint64{10}); err != nil || len(collector.calls) != 2 {
		t.Fatalf("expired snapshot not refreshed: err=%v calls=%d", err, len(collector.calls))
	}
}
