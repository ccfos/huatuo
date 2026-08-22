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

package collector

import (
	"bytes"
	"encoding/binary"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
)

func TestBuildDirectstallMetricsCgroupKeys(t *testing.T) {
	v1 := testReclaimContainer("v1", "v1")
	v2 := testReclaimContainer("v2", "v2")
	keys := map[pod.ContainerCgroupKey]*pod.Container{
		{CSS: 101}:      v1,
		{CgroupID: 201}: v2,
		{CSS: 202}:      v2,
	}

	data, err := buildDirectstallMetrics(keys, []bpf.MapItem{
		memoryReclaimMapItem(t, pod.ContainerCgroupKey{CSS: 101}, 3),
		memoryReclaimMapItem(t, pod.ContainerCgroupKey{CgroupID: 201}, 5),
	})
	if err != nil {
		t.Fatalf("buildDirectstallMetrics() error = %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("metric count = %d, want 2", len(data))
	}

	values := make(map[string]float64, len(data))
	for _, datum := range data {
		values[datum.Labels()[metric.LabelContainerName]] = datum.Value
	}
	if values["v1"] != 3 || values["v2"] != 5 {
		t.Fatalf("metric values = %+v, want v1=3 v2=5", values)
	}

	data, err = buildDirectstallMetrics(keys, nil)
	if err != nil {
		t.Fatalf("buildDirectstallMetrics() with no items error = %v", err)
	}
	if len(data) != 2 || data[0].Value != 0 || data[1].Value != 0 {
		t.Fatalf("zero-value metrics = %+v, want two zero metrics", data)
	}
}

func testReclaimContainer(id, name string) *pod.Container {
	return &pod.Container{
		ID:       id,
		Name:     name,
		Hostname: "node",
		Type:     pod.ContainerTypeNormal,
		Labels:   map[string]any{"HostNamespace": "infra"},
	}
}

func memoryReclaimMapItem(t *testing.T, key pod.ContainerCgroupKey, value uint64) bpf.MapItem {
	t.Helper()

	var keyBuffer, valueBuffer bytes.Buffer
	if err := binary.Write(&keyBuffer, binary.LittleEndian, key); err != nil {
		t.Fatalf("encode cgroup key: %v", err)
	}
	if err := binary.Write(&valueBuffer, binary.LittleEndian, memoryBpfStruct{DirectstallCount: value}); err != nil {
		t.Fatalf("encode directstall value: %v", err)
	}
	return bpf.MapItem{Key: keyBuffer.Bytes(), Value: valueBuffer.Bytes()}
}
