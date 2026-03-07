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

//go:build !didi

package pod

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ContainerQos of the container priority.
type ContainerQos int

// All container priorities.
const (
	containerQosUnknown ContainerQos = iota
	containerQosGuaranteed
	containerQosBurstable
	containerQosBestEffort
	containerQosMax
)

// ContainerQosLevelMin is the minimum priority.
const ContainerQosLevelMin = containerQosUnknown

func parseContainerQos(typ ContainerType, pod *corev1.Pod) (ContainerQos, error) {
	switch pod.Status.QOSClass {
	case corev1.PodQOSBurstable:
		return containerQosBurstable, nil
	case corev1.PodQOSBestEffort:
		return containerQosBestEffort, nil
	case corev1.PodQOSGuaranteed:
		return containerQosGuaranteed, nil
	default:
		return containerQosUnknown, nil
	}
}

func (p ContainerQos) String() string {
	switch p {
	case containerQosBurstable:
		return strings.ToLower(string(corev1.PodQOSBurstable))
	case containerQosBestEffort:
		return strings.ToLower(string(corev1.PodQOSBestEffort))
	case containerQosGuaranteed:
		return strings.ToLower(string(corev1.PodQOSGuaranteed))
	default:
		return "unknown"
	}
}

// MarshalJSON marshal container qos to json.
func (p ContainerQos) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON unmarshal container qos from json.
func (p *ContainerQos) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	switch s {
	case string(corev1.PodQOSBurstable):
		*p = containerQosBurstable
	case string(corev1.PodQOSBestEffort):
		*p = containerQosBestEffort
	case string(corev1.PodQOSGuaranteed):
		*p = containerQosGuaranteed
	default:
		*p = containerQosUnknown
	}

	return nil
}

// Int converts ContainerLevel to int.
func (p ContainerQos) Int() int {
	return int(p)
}
