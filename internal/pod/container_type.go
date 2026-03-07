// Copyright 2025 The HuaTuo Authors
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

package pod

import "encoding/json"

// ContainerType of the container type.
type ContainerType uint32

// All container types.
const (
	ContainerTypeSidecar ContainerType = 1 << iota
	ContainerTypeDaemonSet
	ContainerTypeNode
	ContainerTypeStatic
	ContainerTypeNormal
	ContainerTypeUnknown
	_containerTypeAll
)

// ContainerTypeAll is the mask of all container types.
const ContainerTypeAll = ContainerType(uint32(_containerTypeAll) - 1)

var containerType2String = map[ContainerType]string{
	ContainerTypeSidecar:   "sidecar",
	ContainerTypeDaemonSet: "daemonSet",
	ContainerTypeNormal:    "normal",
	ContainerTypeNode:      "node",
	ContainerTypeStatic:    "static",
	ContainerTypeUnknown:   "unknown",
}

func (t ContainerType) String() string {
	return containerType2String[t]
}

// MarshalJSON marshal container type to json.
func (t ContainerType) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// UnmarshalJSON unmarshal container type from json.
func (t *ContainerType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	switch s {
	case containerType2String[ContainerTypeSidecar]:
		*t = ContainerTypeSidecar
	case containerType2String[ContainerTypeDaemonSet]:
		*t = ContainerTypeDaemonSet
	case containerType2String[ContainerTypeNormal]:
		*t = ContainerTypeNormal
	default:
		*t = ContainerTypeUnknown
	}

	return nil
}

// IsSidecar check if container type is sidecar.
func (t ContainerType) IsSidecar() bool {
	return t == ContainerTypeSidecar
}

// IsDaemonSet check if container type is daemonset.
func (t ContainerType) IsDaemonSet() bool {
	return t == ContainerTypeDaemonSet
}

// IsNormal check if container type is normal container.
func (t ContainerType) IsNormal() bool {
	return t == ContainerTypeNormal
}

// IsUnknown check if container type is unknown.
func (t ContainerType) IsUnknown() bool {
	return t == ContainerTypeUnknown
}
