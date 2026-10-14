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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

type debugEvent struct {
	ContainerID      string    `json:"container_id"`
	StartedTimestamp time.Time `json:"started_timestamp"`
	TracerData       struct {
		Threshold uint64  `json:"threshold"`
		DLoadAvg  float64 `json:"dload_avg"`
	} `json:"tracer_data"`
}

func checkDebugEvents(reader io.Reader, cooldown time.Duration) error {
	if cooldown <= 0 {
		return errors.New("debug validation requires a positive cooldown")
	}
	var events []debugEvent
	if err := json.NewDecoder(reader).Decode(&events); err != nil {
		return fmt.Errorf("decode debug events: %w", err)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].StartedTimestamp.Before(events[j].StartedTimestamp)
	})
	previous := make(map[string]time.Time)
	var thresholdBypassed, cooldownBypassed bool
	for _, event := range events {
		thresholdBypassed = thresholdBypassed || event.TracerData.DLoadAvg <= float64(event.TracerData.Threshold)
		last := previous[event.ContainerID]
		elapsed := event.StartedTimestamp.Sub(last)
		if !last.IsZero() && elapsed > 0 && elapsed < cooldown {
			cooldownBypassed = true
		}
		previous[event.ContainerID] = event.StartedTimestamp
	}
	if !thresholdBypassed {
		return errors.New("no debug event has dload_avg at or below its threshold")
	}
	if !cooldownBypassed {
		return fmt.Errorf("no container has two debug events less than %s apart", cooldown)
	}
	return nil
}
