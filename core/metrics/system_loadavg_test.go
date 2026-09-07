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
	"math"
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/pod"
)

func TestHostRunnableLive(t *testing.T) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		t.Skipf("host procfs unavailable: %v", err)
	}
	if _, err := parseHostRunnable(raw); err != nil {
		t.Fatal(err)
	}
}

func TestContainerLoadAverage(t *testing.T) {
	c := &loadavgCollector{sampling: true, sampleInterval: 15 * time.Second}
	at := time.Unix(100, 0)
	container := &pod.Container{ID: "test", Labels: map[string]any{"HostNamespace": "test"}}
	samples := []containerLoadSample{{container, 2, 3}}
	c.publishContainerLoad(at, samples, nil, nil)
	if data, _, _ := c.cachedContainerLoad(at); len(data) != 2 {
		t.Fatal("baseline emitted averages")
	}
	at = at.Add(17 * time.Second)
	c.publishContainerLoad(at, samples, nil, nil)
	data, err, active := c.cachedContainerLoad(at)
	if err != nil || !active || len(data) != 5 {
		t.Fatalf("cache: data=%v active=%v err=%v", data, active, err)
	}
	values := make(map[string]float64, len(data))
	for _, m := range data {
		values[m.Name()] = m.Value
	}
	for i, window := range []float64{60, 300, 900} {
		name := []string{"container_load1", "container_load5", "container_load15"}[i]
		want := 5 * (1 - math.Exp(-17/window))
		if math.Abs(values[name]-want) > 1e-12 {
			t.Fatalf("%s=%g, want %g", name, values[name], want)
		}
	}
}
