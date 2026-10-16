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
	"flag"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

func TestLoadConfigBoundsTimeConversions(t *testing.T) {
	tests := []struct {
		name      string
		duration  uint64
		threshold uint64
		wantError string
	}{
		{"defaults", 8, 100, ""},
		{"largest duration", maxDurationSeconds, 100, ""},
		{"duration overflow", maxDurationSeconds + 1, 100, "--duration"},
		{"largest threshold", 8, maxScheduleThresholdMs, ""},
		{"threshold overflow", 8, maxScheduleThresholdMs + 1, "--schedule-threshold"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := flag.NewFlagSet(tt.name, flag.ContinueOnError)
			for _, option := range appFlags() {
				if err := option.Apply(set); err != nil {
					t.Fatalf("apply flag: %v", err)
				}
			}
			if err := set.Set(cliFlagDuration, strconv.FormatUint(tt.duration, 10)); err != nil {
				t.Fatalf("set duration: %v", err)
			}
			if err := set.Set(cliFlagSchedThreshold, strconv.FormatUint(tt.threshold, 10)); err != nil {
				t.Fatalf("set threshold: %v", err)
			}

			cfg, filters, err := loadConfig(cli.NewContext(cli.NewApp(), set, nil))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("loadConfig() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.durationSecond != tt.duration || cfg.scheduleThreshold != tt.threshold {
				t.Fatalf("loadConfig() time values = %d, %d, want %d, %d",
					cfg.durationSecond, cfg.scheduleThreshold, tt.duration, tt.threshold)
			}
			if got := filters[bpfFilterEventTimeout].(uint64); got != tt.threshold*uint64(time.Millisecond) {
				t.Fatalf("BPF timeout = %d, want %d", got, tt.threshold*uint64(time.Millisecond))
			}
		})
	}
}
