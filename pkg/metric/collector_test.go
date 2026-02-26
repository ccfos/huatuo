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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/mock"
)

func newTestCollectorManager() *CollectorManager {
	return &CollectorManager{
		collectors: make(map[string]*CollectorWrapper),
		hostname:   "huatuo-dev",
		region:     "huatuo-region",
		scrapeDurationDesc: prometheus.NewDesc(
			prometheus.BuildFQName(DefaultNamespace, "scrape", "collector_duration_seconds"),
			"duration",
			[]string{LabelHost, LabelRegion, "collector"},
			nil,
		),
		scrapeSuccessDesc: prometheus.NewDesc(
			prometheus.BuildFQName(DefaultNamespace, "scrape", "collector_success"),
			"success",
			[]string{LabelHost, LabelRegion, "collector"},
			nil,
		),
	}
}

func readMetrics(ch chan prometheus.Metric) []prometheus.Metric {
	metrics := make([]prometheus.Metric, 0)
	for m := range ch {
		metrics = append(metrics, m)
	}
	return metrics
}

func hasSuccessMetric(metrics []prometheus.Metric) bool {
	for i := range metrics {
		if strings.Contains(metrics[i].Desc().String(), "collector_success") {
			return true
		}
	}
	return false
}

func TestCollectorManagerDescribe(t *testing.T) {
	mgr := newTestCollectorManager()
	ch := make(chan *prometheus.Desc, 2)

	mgr.Describe(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	if count != 2 {
		t.Errorf("Describe() desc count=%d, want 2", count)
	}
}

func TestCollectorManagerDoCollect(t *testing.T) {
	defaultRegion = "huatuo-region"

	tests := []struct {
		name            string
		updateFunc      func() ([]*Data, error)
		wantMetricCount int
	}{
		{
			name: "success returns data metric and scrape metrics",
			updateFunc: func() ([]*Data, error) {
				return []*Data{
					NewGaugeData("cpu_usage", 1, "help", map[string]string{"k": "v"}),
				}, nil
			},
			wantMetricCount: 3,
		},
		{
			name: "no data error returns scrape metrics only",
			updateFunc: func() ([]*Data, error) {
				return nil, ErrNoData
			},
			wantMetricCount: 2,
		},
		{
			name: "normal error returns scrape metrics only",
			updateFunc: func() ([]*Data, error) {
				return nil, errors.New("collector failed")
			},
			wantMetricCount: 2,
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			mgr := newTestCollectorManager()
			mockCollector := NewMockCollector(t)
			mockCollector.On("Update").Return(tests[i].updateFunc()).Once()
			cw := &CollectorWrapper{
				collector: mockCollector,
				mu:        sync.Mutex{},
			}

			ch := make(chan prometheus.Metric, 16)
			mgr.doCollect("cpu", cw, ch)
			close(ch)
			metrics := readMetrics(ch)

			if len(metrics) != tests[i].wantMetricCount {
				t.Errorf("metric count=%d, want %d", len(metrics), tests[i].wantMetricCount)
			}

			if !hasSuccessMetric(metrics) {
				t.Errorf("collector_success metric not found")
			}
		})
	}
}

func TestCollectorManagerCollect(t *testing.T) {
	defaultRegion = "huatuo-region"

	mgr := newTestCollectorManager()
	mockC1 := NewMockCollector(t)
	mockC2 := NewMockCollector(t)
	mockC1.On("Update").Return([]*Data{
		NewGaugeData("m1", 1, "help", map[string]string{}),
	}, nil).Once()
	mockC2.On("Update").Return([]*Data(nil), ErrNoData).Once()
	mgr.collectors = map[string]*CollectorWrapper{
		"c1": {
			collector: mockC1,
			mu:        sync.Mutex{},
		},
		"c2": {
			collector: mockC2,
			mu:        sync.Mutex{},
		},
	}

	ch := make(chan prometheus.Metric, 32)
	mgr.Collect(ch)
	close(ch)
	metrics := readMetrics(ch)

	if len(metrics) != 5 {
		t.Errorf("Collect() metric count=%d, want 5", len(metrics))
	}
}

func TestCollectorWrapperMutex(t *testing.T) {
	mgr := newTestCollectorManager()
	ch := make(chan prometheus.Metric, 64)

	var inFlight int32
	var maxInFlight int32

	mockCollector := NewMockCollector(t)
	mockCollector.
		On("Update").
		Run(func(args mock.Arguments) {
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxInFlight)
				if cur <= prev {
					break
				}
				if atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
					break
				}
			}

			time.Sleep(15 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}).
		Return([]*Data(nil), nil).
		Times(5)

	cw := &CollectorWrapper{
		collector: mockCollector,
		mu:        sync.Mutex{},
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.doCollect("cpu", cw, ch)
		}()
	}
	wg.Wait()
	close(ch)
	_ = readMetrics(ch)

	if atomic.LoadInt32(&maxInFlight) > 1 {
		t.Errorf("collector Update() executed concurrently, maxInFlight=%d", atomic.LoadInt32(&maxInFlight))
	}
}
