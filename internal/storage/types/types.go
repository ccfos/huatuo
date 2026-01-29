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
	"time"
)

// standard Document.
type Document struct {
	Hostname     string    `json:"hostname"`
	Region       string    `json:"region"`
	UploadedTime time.Time `json:"uploaded_time"`
	// equal to `TracerTime`, supported the old version.
	Time string `json:"time"`

	ContainerID            string `json:"container_id,omitempty"`
	ContainerHostname      string `json:"container_hostname,omitempty"`
	ContainerHostNamespace string `json:"container_host_namespace,omitempty"`
	ContainerType          string `json:"container_type,omitempty"`
	ContainerQos           string `json:"container_qos,omitempty"`

	TracerName    string `json:"tracer_name,omitempty"`
	TracerID      string `json:"tracer_id,omitempty"`
	TracerTime    string `json:"tracer_time"`
	TracerRunType string `json:"tracer_type,omitempty"`
	TracerData    any    `json:"tracer_data,omitempty"`
}
