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

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type stalledMetricCollector struct {
	entered chan struct{}
	release chan struct{}
}

func (c *stalledMetricCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- prometheus.NewDesc("stalled_metric", "test metric", nil, nil)
}

func (c *stalledMetricCollector) Collect(ch chan<- prometheus.Metric) {
	c.entered <- struct{}{}
	<-c.release
	ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("stalled_metric", "test metric", nil, nil), prometheus.GaugeValue, 1)
}

func TestMetricsLimitsGatheringAfterRequestCancellation(t *testing.T) {
	collector := &stalledMetricCollector{entered: make(chan struct{}, 16), release: make(chan struct{})}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	server := NewServer(&Config{PromReg: registry})
	released := false
	defer func() {
		if !released {
			close(collector.release)
		}
	}()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			server.engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", http.NoBody))
		}()
		select {
		case <-collector.entered:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("initial gather did not enter collector")
		}
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("canceled HTTP request did not return")
		}
	}
	result := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
		result <- w.Code
	}()
	select {
	case status := <-result:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d, want 503", status)
		}
	case <-collector.entered:
		t.Fatal("another gather entered after the in-flight budget was exhausted")
	case <-time.After(time.Second):
		t.Fatal("over-budget scrape was not rejected promptly")
	}
	close(collector.release)
	released = true
	deadline := time.Now().Add(time.Second)
	for {
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
		if w.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed gathers did not release admission slots")
		}
		time.Sleep(time.Millisecond)
	}
}
