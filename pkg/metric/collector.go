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

package metric

import (
	"os"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"
)

var DefaultNamespace = "huatuo_bamai"

// Collector is the interface a collector has to implement.
//
//go:generate mockery --name=Collector --dir=. --filename=mock_collector_test.go --inpackage --case=underscore
type Collector interface {
	// Get new metrics and expose them via prometheus registry.
	Update() ([]*Data, error)
}

// CollectorWrapper adds a mutex to a Collector for thread-safe access.
type CollectorWrapper struct {
	collector Collector
	mu        sync.Mutex
}

// CollectorManager implements the prometheus.Collector interface.
type CollectorManager struct {
	collectors         map[string]*CollectorWrapper
	hostname           string
	region             string
	scrapeDurationDesc *prometheus.Desc
	scrapeSuccessDesc  *prometheus.Desc
}

func NewCollectorManager(blackListed []string, region string) (*CollectorManager, error) {
	// Init defaultRegion, defaultHostname firstly,
	// NewGaugeData may be used for data caching in tracing.NewRegister.
	hostname, _ := os.Hostname()
	defaultRegion, defaultHostname = region, hostname

	tracings, err := tracing.NewRegister(blackListed)
	if err != nil {
		return nil, err
	}

	collectors := make(map[string]*CollectorWrapper)
	for key, trace := range tracings {
		if trace.Flag&tracing.FlagMetric == 0 {
			continue
		}
		collector := trace.TracingData.(Collector)
		collectors[key] = &CollectorWrapper{
			collector: collector,
			mu:        sync.Mutex{},
		}
	}

	scrapeDurationDesc := prometheus.NewDesc(
		prometheus.BuildFQName(DefaultNamespace, "scrape", "collector_duration_seconds"),
		DefaultNamespace+": Duration of a collector scrape.",
		[]string{LabelHost, LabelRegion, "collector"},
		nil,
	)
	scrapeSuccessDesc := prometheus.NewDesc(
		prometheus.BuildFQName(DefaultNamespace, "scrape", "collector_success"),
		DefaultNamespace+": Whether a collector succeeded.",
		[]string{LabelHost, LabelRegion, "collector"},
		nil,
	)

	return &CollectorManager{
		collectors:         collectors,
		hostname:           hostname,
		region:             region,
		scrapeDurationDesc: scrapeDurationDesc,
		scrapeSuccessDesc:  scrapeSuccessDesc,
	}, nil
}

// Describe implements the prometheus.Collector interface.
func (m *CollectorManager) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.scrapeDurationDesc
	ch <- m.scrapeSuccessDesc
}

// Collect implements the prometheus.Collector interface.
func (m *CollectorManager) Collect(ch chan<- prometheus.Metric) {
	wg := sync.WaitGroup{}
	wg.Add(len(m.collectors))

	for name, c := range m.collectors {
		go func(name string, c *CollectorWrapper) {
			m.doCollect(name, c, ch)
			wg.Done()
		}(name, c)
	}
	wg.Wait()
}

func (m *CollectorManager) doCollect(collectorName string, c *CollectorWrapper, ch chan<- prometheus.Metric) {
	var (
		success float64
		metrics []*Data
		err     error
	)

	begin := time.Now()
	// only one goroutine fetches metrics from a collector at a time
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		metrics, err = c.collector.Update()
	}()

	duration := time.Since(begin)

	if err != nil {
		if IsNoDataError(err) {
			log.Debugf("collector %s returned no data, duration_seconds %f: %v", collectorName, duration.Seconds(), err)
		} else {
			log.Infof("collector %s failed, duration_seconds %f: %v", collectorName, duration.Seconds(), err)
		}
		success = 0
	} else {
		for _, data := range metrics {
			ch <- data.prometheusMetric(collectorName)
		}
		log.Debugf("collector %s succeeded, duration_seconds %f", collectorName, duration.Seconds())
		success = 1
	}

	ch <- prometheus.MustNewConstMetric(m.scrapeDurationDesc, prometheus.GaugeValue, duration.Seconds(), m.hostname, m.region, collectorName)
	ch <- prometheus.MustNewConstMetric(m.scrapeSuccessDesc, prometheus.GaugeValue, success, m.hostname, m.region, collectorName)
}
