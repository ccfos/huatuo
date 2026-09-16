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

// WatchEvent is a CloudEvents 1.0 envelope.
// Field names follow the CloudEvents 1.0 specification (lowercase).
// Data carries the domain payload; consumers should unmarshal it according to Type.
type WatchEvent struct {
	SpecVersion     string `json:"specversion"`
	ID              string `json:"id"`
	Source          string `json:"source"`
	Type            string `json:"type"`
	DataContentType string `json:"datacontenttype"`
	Time            string `json:"time"`
	Data            any    `json:"data"`
}

// WatchEventData is the stable public payload carried inside a WatchEvent.
// It exposes a curated subset of internal Document fields so that internal
// storage changes do not affect the public API contract.
type WatchEventData struct {
	Hostname                string `json:"hostname"`
	Region                  string `json:"region"`
	ObservedTimestamp       string `json:"observed_timestamp"`
	KernelObservedTimestamp string `json:"kernel_observed_timestamp,omitempty"`
	ContainerID             string `json:"container_id,omitempty"`
	ContainerHostname       string `json:"container_hostname,omitempty"`
	ContainerHostNamespace  string `json:"container_host_namespace,omitempty"`
	ContainerType           string `json:"container_type,omitempty"`
	ContainerQos            string `json:"container_qos,omitempty"`
	TracerName              string `json:"tracer_name,omitempty"`
	TracerID                string `json:"tracer_id,omitempty"`
	TracerRunType           string `json:"tracer_run_type,omitempty"`
}
