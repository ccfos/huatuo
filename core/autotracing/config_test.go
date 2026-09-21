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
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
	testutils "github.com/ccfos/huatuo/internal/testing"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "valid config",
		},
		{
			name: "invalid issues list",
			configure: func(cfg *Config) {
				cfg.IssuesList = [][]string{{"missing-expression"}}
			},
			wantError: "validating issues list",
		},
		{
			name: "zero snapshot tracing interval",
			configure: func(cfg *Config) {
				cfg.MemoryThresholdSnapshot.IntervalTracing = 0
			},
			wantError: "tracing interval seconds must be positive",
		},
		{
			name: "negative snapshot tracing interval",
			configure: func(cfg *Config) {
				cfg.MemoryThresholdSnapshot.IntervalTracing = -1
			},
			wantError: "tracing interval seconds must be positive",
		},
		{
			name: "zero snapshot tracing tool timeout",
			configure: func(cfg *Config) {
				cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = 0
			},
			wantError: "tracing tool timeout seconds must be positive",
		},
		{
			name: "negative snapshot tracing tool timeout",
			configure: func(cfg *Config) {
				cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = -1
			},
			wantError: "tracing tool timeout seconds must be positive",
		},
		{
			name: "zero snapshot maximum memory object entries",
			configure: func(cfg *Config) {
				cfg.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 0
			},
			wantError: "snapshot maximum memory object entries must be in [1, 100]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.MemoryThresholdSnapshot.ThresholdPercent = 90
			cfg.MemoryThresholdSnapshot.IntervalTracing = 300
			cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = 2
			cfg.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 10
			if tt.configure != nil {
				tt.configure(cfg)
			}

			err := cfg.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func TestMemoryThresholdSnapshotConfigRejectsOverflowAndUnboundedEntries(t *testing.T) {
	for _, field := range []string{"interval", "tracing timeout", "entries"} {
		t.Run(field, func(t *testing.T) {
			config := &Config{}
			cfg := &config.MemoryThresholdSnapshot
			cfg.ThresholdPercent = 90
			cfg.IntervalTracing = 300
			cfg.RunTracingToolTimeout = 2
			cfg.MaxMemoryObjectEntries = 10
			if err := validateMemoryThresholdSnapshotConfig(config); err != nil {
				t.Fatal(err)
			}
			if field != "entries" && strconv.IntSize != 64 {
				t.Skip("duration overflow requires 64-bit int")
			}
			switch field {
			case "interval":
				maximum := int64(1<<63-1) / int64(time.Second)
				cfg.IntervalTracing = int(maximum + 1)
			case "tracing timeout":
				maximum := int64(1<<63-1) / int64(time.Second)
				cfg.RunTracingToolTimeout = int(maximum + 1)
			case "entries":
				cfg.MaxMemoryObjectEntries = memsnapshot.MaxMemoryObjectEntries + 1
			}
			if err := validateMemoryThresholdSnapshotConfig(config); err == nil {
				t.Fatalf("unbounded %s accepted", field)
			}
		})
	}
}

func TestConfigCloneDoesNotShareMutableReferences(t *testing.T) {
	source := &Config{}
	testutils.PopulateCloneSource(t, source)

	testutils.AssertDeepClone(t, source, source.Clone())
}

func TestSetPublishesIndependentConfig(t *testing.T) {
	src := &Config{IssuesList: [][]string{{"dload", "jbd2"}}}
	Set(src)
	src.IssuesList[0][0] = "cpuidle"

	if got := configSnapshot().IssuesList[0][0]; got != "dload" {
		t.Fatalf("IssuesList[0][0] = %q, want detached value", got)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	testConcurrentSnapshots(t, [][2]int64{{3, 300}, {4, 400}})
}

func testConcurrentSnapshots(t *testing.T, pairs [][2]int64) {
	t.Helper()
	Set(&Config{})
	valid := map[[2]int64]bool{{0, 0}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair [2]int64) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.CPUSys.Interval = pair[0]
				cfg.CPUSys.IntervalTracing = pair[1]
				Set(cfg)
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				cfg := configSnapshot()
				got := [2]int64{cfg.CPUSys.Interval, cfg.CPUSys.IntervalTracing}
				if !valid[got] {
					select {
					case errCh <- fmt.Errorf("observed mixed config snapshot: %v", got):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
