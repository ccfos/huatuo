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
	"time"

	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
)

// ProfilerEvent is the payload sent by the profiler subprocess over toolstream.
type ProfilerEvent struct {
	TracerID      string              `json:"tracer_id,omitempty"`
	ContainerID   string              `json:"container_id,omitempty"`
	TracerName    string              `json:"tracer_name,omitempty"`
	TracerRunType string              `json:"tracer_type,omitempty"`
	TracerTime    string              `json:"tracer_time"`
	TracerData    *profctx.TracerData `json:"tracer_data,omitempty"`
}

const profilerEventTimeLayout = "2006-01-02 15:04:05.000 -0700"

func init() {
	toolstream.RegisterDefault[*ProfilerEvent]("profiler", handleProfilerEvent)
}

func handleProfilerEvent(_ *toolstream.Session, ev *ProfilerEvent) error {
	return tracing.SaveProfile(&tracing.WriteRequest{
		TracerName:    ev.TracerName,
		TracerID:      ev.TracerID,
		ContainerID:   ev.ContainerID,
		TracerTime:    parseProfilerEventTime(ev.TracerTime),
		TracerData:    ev.TracerData,
		TracerRunType: ev.TracerRunType,
	})
}

// parseProfilerEventTime parses the wire-format tracer time, falling back to
// the current time when the field is absent or malformed.
func parseProfilerEventTime(raw string) time.Time {
	if parsed, err := time.Parse(profilerEventTimeLayout, raw); err == nil {
		return parsed
	}

	return time.Now()
}
