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

package pod

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestContainerTypeJSONRoundTrip(t *testing.T) {
	types := []ContainerType{
		ContainerTypeSidecar,
		ContainerTypeDaemonSet,
		ContainerTypeNode,
		ContainerTypeStatic,
		ContainerTypeNormal,
		ContainerTypeUnknown,
	}

	for _, want := range types {
		t.Run(want.String(), func(t *testing.T) {
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			var got ContainerType
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", data, err)
			}
			if got != want {
				t.Errorf("round trip = %v, want %v", got, want)
			}
		})
	}
}

func TestContainerQosJSONRoundTrip(t *testing.T) {
	levels := []ContainerQos{
		containerQosUnknown,
		containerQosGuaranteed,
		containerQosBurstable,
		containerQosBestEffort,
	}

	for _, want := range levels {
		t.Run(want.String(), func(t *testing.T) {
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			var got ContainerQos
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", data, err)
			}
			if got != want {
				t.Errorf("round trip = %v, want %v", got, want)
			}
		})
	}
}

func TestContainerQosUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want ContainerQos
	}{
		{name: "lowercase", data: `"guaranteed"`, want: containerQosGuaranteed},
		{name: "Kubernetes", data: `"Guaranteed"`, want: containerQosGuaranteed},
		{name: "uppercase", data: `"GUARANTEED"`, want: containerQosUnknown},
		{name: "invalid", data: `"invalid"`, want: containerQosUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ContainerQos
			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tt.data, err)
			}
			if got != tt.want {
				t.Errorf("Unmarshal(%s) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestContainerQosUnmarshalJSONRejectsNonString(t *testing.T) {
	var qos ContainerQos
	if err := json.Unmarshal([]byte(`1`), &qos); err == nil {
		t.Fatal("Unmarshal(non-string) error = nil")
	}
}

func TestContainerQosStringInvalid(t *testing.T) {
	for _, qos := range []ContainerQos{-1, containerQosMax} {
		if got := qos.String(); got != "unknown" {
			t.Errorf("ContainerQos(%d).String() = %q, want unknown", qos, got)
		}
	}
}

func TestParseContainerQos(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Status.QOSClass = corev1.PodQOSBestEffort

	got, err := parseContainerQos(ContainerTypeUnknown, pod)
	if err != nil {
		t.Fatalf("parseContainerQos() error = %v", err)
	}
	if got != containerQosBestEffort {
		t.Errorf("parseContainerQos() = %v, want %v", got, containerQosBestEffort)
	}
}

func TestContainerJSONUnknownValues(t *testing.T) {
	var typ ContainerType
	if err := json.Unmarshal([]byte(`"future"`), &typ); err != nil {
		t.Fatalf("Unmarshal(container type) error = %v", err)
	}
	if typ != ContainerTypeUnknown {
		t.Errorf("container type = %v, want unknown", typ)
	}

	var qos ContainerQos
	if err := json.Unmarshal([]byte(`"future"`), &qos); err != nil {
		t.Fatalf("Unmarshal(container qos) error = %v", err)
	}
	if qos != containerQosUnknown {
		t.Errorf("container qos = %v, want unknown", qos)
	}
}
