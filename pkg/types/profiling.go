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

package types

import profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

// ProfilingWindow is the legacy complete pprof envelope. It preserves metadata
// that the scalar aggregation protocol cannot represent.
type ProfilingWindow struct {
	ContainerID    string             `json:"container_id,omitempty"`
	ProfileType    string             `json:"profile_type"`
	Profile        *profilev1.Profile `json:"profile,omitempty"`
	DroppedSamples map[string]uint64  `json:"dropped_samples,omitempty"`
}

const (
	DropReasonAggregationQueue = "aggregation_queue"
	DropReasonCollection       = "collection"
)

// AggregationWindow owns immutable dictionaries for one delivery interval.
type AggregationWindow struct {
	ContainerID    string            `json:"container_id,omitempty"`
	ProfileType    string            `json:"profile_type"`
	DroppedSamples map[string]uint64 `json:"dropped_samples,omitempty"`
	Aggregated     AggregatedProfile `json:"aggregated"`
}

type ProfileFrame struct {
	Name string `json:"name"`
	File string `json:"file,omitempty"`
	Line int64  `json:"line,omitempty"`
}

type ProfileLabelKind uint8

const (
	ProfileLabelUnknown ProfileLabelKind = iota
	ProfileLabelString
	ProfileLabelNumber
)

type ProfileLabel struct {
	Name        string           `json:"name"`
	Kind        ProfileLabelKind `json:"kind"`
	StringValue string           `json:"string_value,omitempty"`
	NumberValue int64            `json:"number_value,omitempty"`
	NumberUnit  string           `json:"number_unit,omitempty"`
}

type AggregatedProfile struct {
	TimeNanos     int64              `json:"time_nanos"`
	DurationNanos int64              `json:"duration_nanos"`
	Frames        []ProfileFrame     `json:"frames"`
	Stacks        [][]uint32         `json:"stacks"`
	LabelSets     [][]ProfileLabel   `json:"label_sets"`
	Samples       []AggregatedSample `json:"samples"`
}

// Stack and label IDs are one-based; zero denotes empty content.
// Frame IDs are zero-based. IDs never cross a window boundary.
type AggregatedSample struct {
	UserStack   uint32 `json:"user_stack"`
	KernelStack uint32 `json:"kernel_stack"`
	LabelSet    uint32 `json:"label_set"`
	Value       int64  `json:"value"`
}
