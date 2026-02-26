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
	"sync"
	"testing"

	"huatuo-bamai/internal/pod"
)

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
