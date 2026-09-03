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

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/timeutil"
)

const (
	// TracerRunTypeProfiling identifies an API-requested profiling observation.
	TracerRunTypeProfiling = "profiling"
	// TracerRunTypeTracing identifies an API-requested tracing observation.
	TracerRunTypeTracing = "tracing"
	// TracerRunTypeAutotracing identifies an automatically triggered observation.
	TracerRunTypeAutotracing = "autotracing"
	// TracerRunTypeEvent identifies an event-triggered observation.
	TracerRunTypeEvent = "event"
)

// Document field names are shared so storage indexes cannot drift from the
// serialized contract.
const (
	DocumentFieldHostname                = "hostname"
	DocumentFieldNodeIP                  = "node_ip"
	DocumentFieldRegion                  = "region"
	DocumentFieldUploadedTimestamp       = "uploaded_timestamp"
	DocumentFieldStartedTimestamp        = "started_timestamp"
	DocumentFieldObservedTimestamp       = "observed_timestamp"
	DocumentFieldKernelObservedTimestamp = "kernel_observed_timestamp"
	DocumentFieldContainerID             = "container_id"
	DocumentFieldContainerHostname       = "container_hostname"
	DocumentFieldContainerHostNamespace  = "container_host_namespace"
	DocumentFieldContainerType           = "container_type"
	DocumentFieldContainerQoS            = "container_qos"
	DocumentFieldTracerName              = "tracer_name"
	DocumentFieldTracerID                = "tracer_id"
	DocumentFieldTracerType              = "tracer_type"
)

// Document contains fields shared by persisted tracing and profiling documents.
//
// Its timestamps have distinct owners and must not be used as fallbacks for one
// another:
//   - UploadedTimestamp is assigned by storage immediately before persistence.
//   - StartedTimestamp is supplied by profiling, tracing, and autotracing
//     producers and marks the beginning of the represented observation.
//   - ObservedTimestamp marks when the event producer observed the event in userspace.
//   - KernelObservedTimestamp is optional and marks when the kernel observed the
//     event, converted from the host monotonic clock to UTC by the producer.
//
// TracerRunType determines which producer-owned timestamp is required.
type Document struct {
	Hostname                string              `json:"hostname"`
	NodeIP                  string              `json:"node_ip,omitempty"`
	Region                  string              `json:"region"`
	UploadedTimestamp       timeutil.Timestamp  `json:"uploaded_timestamp"`
	StartedTimestamp        *timeutil.Timestamp `json:"started_timestamp,omitempty"`
	ObservedTimestamp       *timeutil.Timestamp `json:"observed_timestamp,omitempty"`
	KernelObservedTimestamp *timeutil.Timestamp `json:"kernel_observed_timestamp,omitempty"`

	ContainerID            string `json:"container_id,omitempty"`
	ContainerHostname      string `json:"container_hostname,omitempty"`
	ContainerHostNamespace string `json:"container_host_namespace,omitempty"`
	ContainerType          string `json:"container_type,omitempty"`
	ContainerQoS           string `json:"container_qos,omitempty"`

	TracerName    string `json:"tracer_name,omitempty"`
	TracerID      string `json:"tracer_id,omitempty"`
	TracerRunType string `json:"tracer_type,omitempty"`
}

// Validate checks the timestamp invariant selected by TracerRunType.
func (d *Document) Validate() error {
	if d == nil {
		return errors.New("document is required")
	}
	if d.UploadedTimestamp.IsZero() {
		return errors.New("document uploaded timestamp is required")
	}

	switch d.TracerRunType {
	case TracerRunTypeProfiling, TracerRunTypeTracing, TracerRunTypeAutotracing:
		if d.StartedTimestamp == nil || d.StartedTimestamp.IsZero() {
			return fmt.Errorf("document started timestamp is required for tracer type %q", d.TracerRunType)
		}
	case TracerRunTypeEvent:
		if d.ObservedTimestamp == nil || d.ObservedTimestamp.IsZero() {
			return errors.New("document observed timestamp is required for tracer type \"event\"")
		}
	default:
		return fmt.Errorf("document tracer type %q is not supported", d.TracerRunType)
	}

	return nil
}
