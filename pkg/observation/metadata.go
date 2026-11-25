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

package observation

// Field names are shared by metric labels, document JSON, and storage queries.
const (
	FieldRegion             = "region"
	FieldHostname           = "hostname"
	FieldContainerID        = "container_id"
	FieldContainerName      = "container_name"
	FieldContainerHostname  = "container_hostname"
	FieldContainerNamespace = "container_namespace"
	FieldContainerType      = "container_type"
	FieldContainerQoS       = "container_qos"
)

// Metadata identifies the node and optional container represented by an observation.
// Timestamps belong to their observations, not to the identity fields.
type Metadata struct {
	Region             string `json:"region"`
	Hostname           string `json:"hostname"`
	ContainerID        string `json:"container_id,omitempty"`
	ContainerName      string `json:"container_name,omitempty"`
	ContainerHostname  string `json:"container_hostname,omitempty"`
	ContainerNamespace string `json:"container_namespace,omitempty"`
	ContainerType      string `json:"container_type,omitempty"`
	ContainerQoS       string `json:"container_qos,omitempty"`
}

// IsMetadataField reports whether a field is owned by observation metadata.
func IsMetadataField(name string) bool {
	switch name {
	case FieldRegion, FieldHostname, FieldContainerID, FieldContainerName,
		FieldContainerHostname, FieldContainerNamespace, FieldContainerType, FieldContainerQoS:
		return true
	default:
		return false
	}
}

// AddFields writes public metadata into a new document field map. Empty optional
// attributes are omitted, matching Metadata's JSON representation.
func (m *Metadata) AddFields(fields map[string]any) {
	fields[FieldRegion] = m.Region
	fields[FieldHostname] = m.Hostname

	if m.ContainerID != "" {
		fields[FieldContainerID] = m.ContainerID
	}

	if m.ContainerName != "" {
		fields[FieldContainerName] = m.ContainerName
	}

	if m.ContainerHostname != "" {
		fields[FieldContainerHostname] = m.ContainerHostname
	}

	if m.ContainerNamespace != "" {
		fields[FieldContainerNamespace] = m.ContainerNamespace
	}

	if m.ContainerType != "" {
		fields[FieldContainerType] = m.ContainerType
	}

	if m.ContainerQoS != "" {
		fields[FieldContainerQoS] = m.ContainerQoS
	}
}
