// Copyright 2025, 2026 The HuaTuo Authors
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

var containerQosStrings = [...]string{
	containerQosUnknown:    "unknown",
	containerQosGuaranteed: "guaranteed",
	containerQosBurstable:  "burstable",
	containerQosBestEffort: "besteffort",
}

var containerQosByString = map[string]ContainerQos{
	"unknown":    containerQosUnknown,
	"guaranteed": containerQosGuaranteed,
	"burstable":  containerQosBurstable,
	"besteffort": containerQosBestEffort,

	string(corev1.PodQOSGuaranteed): containerQosGuaranteed,
	string(corev1.PodQOSBurstable):  containerQosBurstable,
	string(corev1.PodQOSBestEffort): containerQosBestEffort,
}

func parseContainerQos(_ ContainerType, pod *corev1.Pod) (ContainerQos, error) {
	return containerQosByString[string(pod.Status.QOSClass)], nil
}

func (p ContainerQos) String() string {
	if p < containerQosUnknown || p >= containerQosMax {
		return containerQosStrings[containerQosUnknown]
	}

	return containerQosStrings[p]
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

	*p = containerQosByString[s]
	return nil
}

// Int converts ContainerLevel to int.
func (p ContainerQos) Int() int {
	return int(p)
}
