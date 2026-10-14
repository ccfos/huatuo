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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCheckDebugEvents(t *testing.T) {
	event := func(id string, elapsed time.Duration, load float64) debugEvent {
		e := debugEvent{ContainerID: id, StartedTimestamp: time.Unix(0, 0).UTC().Add(elapsed)}
		e.TracerData.Threshold = 8
		e.TracerData.DLoadAvg = load
		return e
	}
	for _, tc := range []struct {
		name   string
		events []debugEvent
		want   string
	}{
		{"empty", []debugEvent{}, "at or below"},
		{"one event", []debugEvent{event("a", 0, 0)}, "two debug events"},
		{"different containers", []debugEvent{event("a", 0, 0), event("b", time.Second, 0)}, "two debug events"},
		{"any container", []debugEvent{event("coredns", 0, 0), event("coredns", time.Second, 0)}, ""},
		{"mixed containers", []debugEvent{event("target", 0, 0), event("coredns", time.Second, 0), event("coredns", 2*time.Second, 0)}, ""},
		{"threshold not bypassed", []debugEvent{event("a", 0, 9), event("a", time.Second, 9)}, "at or below"},
		{"threshold boundary", []debugEvent{event("a", 0, 8), event("a", time.Second, 9)}, ""},
		{"cooldown boundary", []debugEvent{event("a", 0, 0), event("a", time.Minute, 0)}, "two debug events"},
		{"cooldown exceeded", []debugEvent{event("a", 0, 0), event("a", 61*time.Second, 0)}, "two debug events"},
		{"fractional cooldown boundary", []debugEvent{event("a", 0, 0), event("a", time.Minute-time.Nanosecond, 0)}, ""},
		{"same timestamp", []debugEvent{event("a", 0, 0), event("a", 0, 0)}, "two debug events"},
		{"out of order", []debugEvent{event("a", 2*time.Second, 0), event("a", 0, 0), event("a", time.Second, 0)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.events)
			if err != nil {
				t.Fatal(err)
			}
			err = checkDebugEvents(bytes.NewReader(data), time.Minute)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkDebugEvents() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckDebugEventsInvalidInput(t *testing.T) {
	for _, cooldown := range []time.Duration{0, -time.Second} {
		if err := checkDebugEvents(strings.NewReader("[]"), cooldown); err == nil || !strings.Contains(err.Error(), "positive cooldown") {
			t.Fatalf("checkDebugEvents(%s) error = %v, want invalid cooldown", cooldown, err)
		}
	}
	for _, data := range []string{"invalid", `[{"started_timestamp":"invalid"}]`} {
		if err := checkDebugEvents(strings.NewReader(data), time.Minute); err == nil || !strings.Contains(err.Error(), "decode debug events") {
			t.Fatalf("checkDebugEvents(%q) error = %v, want decoding failure", data, err)
		}
	}
}
