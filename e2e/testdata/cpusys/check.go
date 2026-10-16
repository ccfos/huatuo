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
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/ccfos/huatuo/internal/flamegraph"
)

func checkStatus(reader io.Reader, disabled bool) error {
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		return err
	}
	found := 0
	for _, name := range []string{"huatuo_bamai_tracing_status_running", "huatuo_bamai_tracing_status_hitcount"} {
		family := families[name]
		if disabled && name == "huatuo_bamai_tracing_status_hitcount" && family == nil {
			continue
		}
		if family == nil || family.GetType() != dto.MetricType_GAUGE {
			return fmt.Errorf("missing gauge %s", name)
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string)
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			if labels["host"] != "huatuo-dev" || labels["region"] != "e2e" {
				return fmt.Errorf("%s has incorrect host/region labels", name)
			}
			if name == "huatuo_bamai_tracing_status_hitcount" {
				if labels["tracing"] != "cpusys" || metric.GetGauge().GetValue() != 0 {
					return errors.New("unexpected tracer or cpusys sampling loop restarted")
				}
				found++
			} else {
				want := float64(1)
				if disabled {
					want = 0
				}
				if len(family.GetMetric()) != 1 || metric.GetGauge().GetValue() != want {
					return fmt.Errorf("running tracer count must be %g", want)
				}
			}
		}
	}
	want := 1
	if disabled {
		want = 0
	}
	if found != want {
		return fmt.Errorf("cpusys lifecycle count=%d, disabled=%t", found, disabled)
	}
	return nil
}

type event struct {
	TracerName        string    `json:"tracer_name"`
	TracerType        string    `json:"tracer_type"`
	TracerID          string    `json:"tracer_id"`
	Hostname          string    `json:"hostname"`
	Region            string    `json:"region"`
	ContainerID       string    `json:"container_id"`
	StartedTimestamp  time.Time `json:"started_timestamp"`
	UploadedTimestamp time.Time `json:"uploaded_timestamp"`
	TracerData        struct {
		SystemPercent               int64                  `json:"system_percent"`
		SystemPercentThreshold      int64                  `json:"system_percent_threshold"`
		SystemPercentDelta          int64                  `json:"system_percent_delta"`
		SystemPercentDeltaThreshold int64                  `json:"system_percent_delta_threshold"`
		FlameData                   []flamegraph.FrameData `json:"flamedata"`
	} `json:"tracer_data"`
}

func checkEvents(data []byte, comm string, usage, delta int64, cooldown time.Duration) error {
	var events []event
	if err := json.Unmarshal(data, &events); err != nil {
		return err
	}
	if len(events) == 0 || comm == "" {
		return errors.New("event validation requires events and a workload comm")
	}
	seen := make(map[string]bool)
	for i := range events {
		e := &events[i]
		if e.TracerName != "cpusys" || e.TracerType != "autotracing" || e.Hostname != "huatuo-dev" ||
			e.Region != "e2e" || e.ContainerID != "" || e.TracerID == "" || seen[e.TracerID] {
			return fmt.Errorf("event %d: incorrect host metadata or duplicate tracer ID", i)
		}
		seen[e.TracerID] = true
		if e.StartedTimestamp.IsZero() || e.UploadedTimestamp.Before(e.StartedTimestamp) ||
			time.Since(e.StartedTimestamp).Abs() > 10*time.Minute {
			return fmt.Errorf("event %d: invalid start/upload timestamps", i)
		}
		if i > 0 && cooldown > 0 && e.StartedTimestamp.Sub(events[i-1].StartedTimestamp) < cooldown {
			return fmt.Errorf("event %d: tracing repeated within %s cooldown", i, cooldown)
		}
		d := &e.TracerData
		if d.SystemPercent <= usage || d.SystemPercent > 100 || d.SystemPercentDelta <= delta ||
			d.SystemPercentDelta > d.SystemPercent || d.SystemPercentThreshold != usage ||
			d.SystemPercentDeltaThreshold != delta {
			return fmt.Errorf("event %d: invalid system percentage/delta or configured thresholds", i)
		}
		if err := checkFrames(d.FlameData, comm); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
	}
	return nil
}

func checkFrames(frames []flamegraph.FrameData, comm string) error {
	if len(frames) < 2 || frames[0].Level != 0 {
		return errors.New("flame graph has no root and sampled workload")
	}
	parents := make([]int, 0, len(frames))
	children := make([]int64, len(frames))
	foundWorker, foundKernel := false, false
	workerLevel := int64(-1)
	for i := range frames {
		frame := &frames[i]
		if frame.Level < 0 || frame.Level > int64(len(parents)) || (i > 0 && frame.Level == 0) ||
			frame.Value <= 0 || frame.Self < 0 || frame.Self > frame.Value || frame.Label == "" {
			return fmt.Errorf("invalid flame frame %d: %+v", i, frame)
		}
		parents = parents[:int(frame.Level)]
		if len(parents) > 0 {
			children[parents[len(parents)-1]] += frame.Value
		}
		parents = append(parents, i)
		if frame.Level <= workerLevel {
			workerLevel = -1
		}
		if frame.Label == comm {
			foundWorker, workerLevel = true, frame.Level
		}
		foundKernel = foundKernel || (workerLevel >= 0 && strings.HasSuffix(frame.Label, "_[k]"))
	}
	for i := range frames {
		if frames[i].Self+children[i] != frames[i].Value {
			return fmt.Errorf("frame %d: value=%d differs from self+children=%d", i, frames[i].Value, frames[i].Self+children[i])
		}
	}
	if !foundWorker || !foundKernel {
		return fmt.Errorf("flame graph worker=%t worker-kernel=%t, want comm=%q with a kernel frame", foundWorker, foundKernel, comm)
	}
	return nil
}
