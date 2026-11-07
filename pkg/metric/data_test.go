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

package metric

import (
	"errors"
	"maps"
	"reflect"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/internal/pod"
)

func TestDefaultHostnameAndRegion(t *testing.T) {
	defaultRegion = "huatuo-region"
	defaultHostname = "huatuo-dev"

	if DefaultHostname() != "huatuo-dev" {
		t.Errorf("DefaultHostname()=%q, want %q", DefaultHostname(), "huatuo-dev")
	}
	if DefaultRegion() != "huatuo-region" {
		t.Errorf("DefaultRegion()=%q, want %q", DefaultRegion(), "huatuo-region")
	}

	defaultRegion = ""
	defaultHostname = ""
	if DefaultHostname() != "" {
		t.Errorf("DefaultHostname()=%q, want empty", DefaultHostname())
	}
	if DefaultRegion() != "" {
		t.Errorf("DefaultRegion()=%q, want empty", DefaultRegion())
	}
}

func TestIsNoDataError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		validate func(t *testing.T, got bool)
	}{
		{
			name: "direct no data",
			err:  ErrNoData,
			validate: func(t *testing.T, got bool) {
				if !got {
					t.Errorf("IsNoDataError(ErrNoData) = false, want true")
				}
			},
		},
		{
			name: "wrapped no data",
			err:  errors.Join(errors.New("wrapped"), ErrNoData),
			validate: func(t *testing.T, got bool) {
				if !got {
					t.Errorf("IsNoDataError(wrapped ErrNoData) = false, want true")
				}
			},
		},
		{
			name: "other error",
			err:  errors.New("other"),
			validate: func(t *testing.T, got bool) {
				if got {
					t.Errorf("IsNoDataError(other) = true, want false")
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			tests[i].validate(t, IsNoDataError(tests[i].err))
		})
	}
}

func TestNewGaugeData(t *testing.T) {
	defaultRegion = "huatuo-region"
	defaultHostname = "huatuo-dev"

	tests := []struct {
		name     string
		build    func() *Data
		validate func(t *testing.T, d *Data)
	}{
		{
			name: "valid gauge with sorted labels",
			build: func() *Data {
				return NewGaugeData("cpu_usage", 1.25, "cpu usage", map[string]string{"z": "2", "a": "1"})
			},
			validate: func(t *testing.T, d *Data) {
				if d == nil {
					t.Errorf("NewGaugeData() = nil, want non-nil")
					return
				}
				if d.valueType != MetricTypeGauge {
					t.Errorf("valueType=%d, want %d", d.valueType, MetricTypeGauge)
				}
				if d.Value != 1.25 {
					t.Errorf("Value=%f, want %f", d.Value, 1.25)
				}
				if len(d.labelKey) != 4 || len(d.labelValue) != 4 {
					t.Errorf("labels length=%d/%d, want 4/4", len(d.labelKey), len(d.labelValue))
					return
				}
				if d.labelKey[0] != LabelRegion || d.labelValue[0] != "huatuo-region" {
					t.Errorf("label[0]=%q:%q, want %q:%q", d.labelKey[0], d.labelValue[0], LabelRegion, "huatuo-region")
				}
				if d.labelKey[1] != LabelHost || d.labelValue[1] == "" {
					t.Errorf("label[1]=%q:%q, want host with non-empty value", d.labelKey[1], d.labelValue[1])
				}
				if d.labelKey[2] != "a" || d.labelKey[3] != "z" {
					t.Errorf("custom label keys=%v, want sorted [a z]", d.labelKey[2:])
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			tests[i].validate(t, tests[i].build())
		})
	}
}

func TestDataMetadata(t *testing.T) {
	data := NewGaugeData(
		"cpu_usage",
		1.25,
		"cpu usage",
		map[string]string{"device": "sda"},
	)

	if data.Name() != "cpu_usage" {
		t.Errorf("Name()=%q, want %q", data.Name(), "cpu_usage")
	}
	if data.Type() != MetricTypeGauge {
		t.Errorf("Type()=%d, want %d", data.Type(), MetricTypeGauge)
	}
	if data.Help() != "cpu usage" {
		t.Errorf("Help()=%q, want %q", data.Help(), "cpu usage")
	}

	labels := data.Labels()
	if labels["device"] != "sda" {
		t.Errorf("Labels()[device]=%q, want %q", labels["device"], "sda")
	}
	labels["device"] = "changed"
	if data.Labels()["device"] != "sda" {
		t.Error("Labels() returned mutable internal state")
	}
}

func TestNewCounterData_DiffPoint(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *Data
		validate func(t *testing.T, d *Data)
	}{
		{
			name: "counter type",
			build: func() *Data {
				return NewCounterData("cpu_total", 9, "cpu total", map[string]string{"k": "v"})
			},
			validate: func(t *testing.T, d *Data) {
				if d == nil {
					t.Errorf("NewCounterData() = nil, want non-nil")
					return
				}
				if d.valueType != MetricTypeCounter {
					t.Errorf("valueType=%d, want %d", d.valueType, MetricTypeCounter)
				}
				if d.Value != 9 {
					t.Errorf("Value=%f, want %f", d.Value, 9.0)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			tests[i].validate(t, tests[i].build())
		})
	}
}

func TestDataLabelsAllowDefaultOverrides(t *testing.T) {
	defaultRegion = "huatuo-region"
	defaultHostname = "huatuo-dev"

	tests := []struct {
		name  string
		build func() *Data
		want  map[string]string
	}{
		{
			name: "host metric",
			build: func() *Data {
				return NewGaugeData("cpu_usage", 1, "cpu usage", map[string]string{
					LabelRegion:        "custom-region",
					LabelHost:          "custom-host",
					LabelContainerName: "custom-container",
				})
			},
			want: map[string]string{
				LabelRegion:        "custom-region",
				LabelHost:          "custom-host",
				LabelContainerName: "custom-container",
			},
		},
		{
			name: "container metric",
			build: func() *Data {
				return NewContainerGaugeData(&pod.Container{
					Name:     "container",
					Hostname: "node",
					Type:     pod.ContainerTypeNormal,
					Labels:   map[string]any{"HostNamespace": "host-ns"},
				}, "latency", 1, "latency", map[string]string{
					LabelContainerName: "custom-container",
					LabelHost:          "custom-host",
				})
			},
			want: map[string]string{LabelContainerName: "custom-container", LabelHost: "custom-host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.build()
			seen := make(map[string]string, len(d.labelKey))
			for i, key := range d.labelKey {
				if _, ok := seen[key]; ok {
					t.Fatalf("duplicate label %q in %v", key, d.labelKey)
				}
				seen[key] = d.labelValue[i]
			}
			for key, want := range tt.want {
				if got := seen[key]; got != want {
					t.Errorf("label %q = %q, want %q", key, got, want)
				}
			}
			if got := d.prometheusMetric("collector"); got == nil {
				t.Error("prometheusMetric() = nil, want non-nil")
			}
		})
	}
}

func TestNewContainerGaugeData(t *testing.T) {
	defaultRegion = "huatuo-region"
	defaultHostname = "huatuo-dev"

	container := &pod.Container{
		Name:     "container",
		Hostname: "huatuo-dev",
		Type:     pod.ContainerTypeNormal,
		Labels: map[string]any{
			"HostNamespace": "host-ns-1001",
		},
	}

	d := NewContainerGaugeData(container, "latency", 5, "latency help", map[string]string{"k2": "v2", "k1": "v1"})
	if d == nil {
		t.Errorf("NewContainerGaugeData() = nil, want non-nil")
		return
	}
	if d.valueType != MetricTypeGauge {
		t.Errorf("valueType=%d, want %d", d.valueType, MetricTypeGauge)
	}
	if len(d.name) < len("container_") || d.name[:len("container_")] != "container_" {
		t.Errorf("name=%q, want prefix %q", d.name, "container_")
	}
	if len(d.labelKey) < 9 {
		t.Errorf("label keys length=%d, want >= 9", len(d.labelKey))
		return
	}
	if d.labelKey[len(d.labelKey)-2] != "k1" || d.labelKey[len(d.labelKey)-1] != "k2" {
		t.Errorf("custom label keys=%v, want suffix [k1 k2]", d.labelKey)
	}
}

func TestPrometheusMetric(t *testing.T) {
	defaultRegion = "huatuo-region"
	metricDescCache = sync.Map{}

	tests := []struct {
		name     string
		build    func() *Data
		validate func(t *testing.T, got any)
	}{
		{
			name: "gauge creates first desc",
			build: func() *Data {
				return &Data{
					name:       "cache_test",
					valueType:  MetricTypeGauge,
					Value:      1,
					help:       "help",
					labelKey:   []string{LabelRegion, LabelHost},
					labelValue: []string{"huatuo-region", "huatuo-dev"},
				}
			},
			validate: func(t *testing.T, got any) {
				if got == nil {
					t.Errorf("prometheusMetric() = nil, want non-nil")
				}
				count := 0
				metricDescCache.Range(func(_, _ any) bool {
					count++
					return true
				})
				if count != 1 {
					t.Errorf("metricDescCache count=%d, want 1", count)
				}
			},
		},
		{
			name: "counter reuses same desc",
			build: func() *Data {
				return &Data{
					name:       "cache_test",
					valueType:  MetricTypeCounter,
					Value:      1,
					help:       "help",
					labelKey:   []string{LabelRegion, LabelHost},
					labelValue: []string{"huatuo-region", "huatuo-dev"},
				}
			},
			validate: func(t *testing.T, got any) {
				if got == nil {
					t.Errorf("prometheusMetric() = nil, want non-nil")
				}
				count := 0
				metricDescCache.Range(func(_, _ any) bool {
					count++
					return true
				})
				if count != 1 {
					t.Errorf("metricDescCache count=%d, want 1", count)
				}
			},
		},
		{
			name: "different metric adds desc",
			build: func() *Data {
				return &Data{
					name:       "cache_test_2",
					valueType:  MetricTypeGauge,
					Value:      1,
					help:       "help",
					labelKey:   []string{LabelRegion, LabelHost},
					labelValue: []string{"huatuo-region", "huatuo-dev"},
				}
			},
			validate: func(t *testing.T, got any) {
				if got == nil {
					t.Errorf("prometheusMetric() = nil, want non-nil")
				}
				count := 0
				metricDescCache.Range(func(_, _ any) bool {
					count++
					return true
				})
				if count != 2 {
					t.Errorf("metricDescCache count=%d, want 2", count)
				}
			},
		},
		{
			name: "invalid value type",
			build: func() *Data {
				return &Data{
					name:       "cache_test_2",
					valueType:  -1,
					Value:      1,
					help:       "help",
					labelKey:   []string{LabelRegion, LabelHost},
					labelValue: []string{"huatuo-region", "huatuo-dev"},
				}
			},
			validate: func(t *testing.T, got any) {
				if got != nil {
					t.Errorf("prometheusMetric() with invalid type = %v, want nil", got)
				}
				count := 0
				metricDescCache.Range(func(_, _ any) bool {
					count++
					return true
				})
				if count != 2 {
					t.Errorf("metricDescCache count=%d, want 2", count)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			tests[i].validate(t, tests[i].build().prometheusMetric("collector"))
		})
	}
}

func BenchmarkContainerGaugeData(b *testing.B) {
	container := &pod.Container{
		Name: "checkout-api", Hostname: "huatuo-dev", Type: pod.ContainerTypeNormal,
		Labels: map[string]any{pod.LabelHostNamespace: "checkout"},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = NewContainerGaugeData(container, "total", 25, "CPU utilization", nil)
	}
}

// TestNewPodGaugeData 验证容器输出与旧入口完全一致，两个沙箱来源共用 workload
// 前缀与六个固定标签；缺失名称/命名空间保持标签集合，额外 CPU 标签保留且身份不可覆盖。
func TestNewPodGaugeData(t *testing.T) {
	labels := map[string]string{LabelHost: "huatuo-dev", LabelRegion: "huatuo-region", "cpu": "2"}
	legacy := &pod.Container{
		Name: "checkout-api", Hostname: "checkout-host", Type: pod.ContainerTypeNormal,
		Qos: pod.ContainerQosLevelMin, Labels: map[string]any{pod.LabelHostNamespace: "checkout"},
	}
	container := &pod.Pod{
		Type: pod.TypeContainer, Name: legacy.Name, Labels: legacy.Labels,
		Container: &pod.ContainerInfo{Hostname: legacy.Hostname, Type: legacy.Type, Qos: legacy.Qos},
	}
	want := NewContainerGaugeData(legacy, "total", 25, "CPU utilization", labels)
	got := NewPodGaugeData(container, "total", 25, "CPU utilization", labels)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("container output changed: got=%+v want=%+v", got, want)
	}
	for _, kind := range []string{pod.TypeCubeSandbox, pod.TypeOpenSandbox} {
		t.Run(kind, func(t *testing.T) {
			workload := &pod.Pod{ID: "checkout-sandbox", Type: kind, Labels: map[string]any{"private": "not-a-metric-label"}}
			for _, named := range []bool{false, true} {
				if named {
					workload.Name = "checkout"
					workload.Labels[pod.LabelWorkloadNamespace] = "production"
				}
				input := maps.Clone(labels)
				input[LabelWorkloadID] = "untrusted-override"
				metric := NewPodGaugeData(workload, "total", 25, "CPU utilization", input)
				name, namespace := "checkout-sandbox", ""
				if named {
					name, namespace = "checkout", "production"
				}
				wantLabels := map[string]string{
					LabelHost: "huatuo-dev", LabelRegion: "huatuo-region", "cpu": "2",
					LabelWorkloadID: workload.ID, LabelWorkloadType: kind, LabelWorkloadName: name, LabelWorkloadNamespace: namespace,
				}
				if metric.Name() != "workload_total" || metric.Type() != MetricTypeGauge || metric.Value != 25 ||
					!maps.Equal(metric.Labels(), wantLabels) || len(metric.labelKey) != len(wantLabels) {
					t.Errorf("sandbox output=%+v, labels=%v", metric, metric.Labels())
				}
				if input[LabelWorkloadID] != "untrusted-override" {
					t.Error("metric builder mutated caller labels")
				}
			}
		})
	}
}

func BenchmarkPodGaugeData(b *testing.B) {
	for _, kind := range []string{pod.TypeContainer, pod.TypeCubeSandbox} {
		b.Run(kind, func(b *testing.B) {
			workload := &pod.Pod{
				ID: "checkout-api", Name: "checkout-api", Type: kind,
				Container: &pod.ContainerInfo{Hostname: "huatuo-dev", Type: pod.ContainerTypeNormal},
				Labels:    map[string]any{pod.LabelHostNamespace: "checkout"},
			}
			b.ReportAllocs()
			for b.Loop() {
				_ = NewPodGaugeData(workload, "total", 25, "CPU utilization", nil)
			}
		})
	}
}

// TestNewPodCounterData 验证容器 counter 与旧入口完全一致，沙箱沿用固定身份、
// 名称回退和 zone 标签，并保持 counter 类型而非 gauge。
func TestNewPodCounterData(t *testing.T) {
	for _, kind := range []string{pod.TypeContainer, pod.TypeCubeSandbox, pod.TypeOpenSandbox} {
		t.Run(kind, func(t *testing.T) {
			workload := &pod.Pod{ID: "checkout-sandbox", Type: kind, Labels: map[string]any{pod.LabelHostNamespace: "production"}, Container: &pod.ContainerInfo{Hostname: "huatuo-dev", Type: pod.ContainerTypeNormal, Qos: pod.ContainerQosLevelMin}}
			labels := map[string]string{LabelHost: "huatuo-dev", LabelRegion: "huatuo-region", "zone": "2"}
			got := NewPodCounterData(workload, "latency", 12, "CPU queue latency", labels)
			want := NewPodGaugeData(workload, "latency", 12, "CPU queue latency", labels)
			want.valueType = MetricTypeCounter
			if kind == pod.TypeContainer {
				want = NewContainerCounterData(&pod.Container{Hostname: "huatuo-dev", Type: pod.ContainerTypeNormal, Qos: pod.ContainerQosLevelMin, Labels: workload.Labels}, "latency", 12, "CPU queue latency", labels)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("counter=%+v want=%+v", got, want)
			}
		})
	}
}
