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

package events

import (
	"sync"
	"sync/atomic"
	"testing"

	"huatuo-bamai/pkg/metric"
)

// doCollect releases the collector mutex after Update and reads Data.Value
// outside of it (pkg/metric/collector.go), so data returned by an earlier
// scrape must not be mutated by a later Update.
func TestSoftLockupUpdateDataNotSharedAcrossScrapes(t *testing.T) {
	attr, err := newSoftLockup()
	if err != nil {
		t.Fatalf("newSoftLockup() error = %v", err)
	}
	c := attr.TracingData.(*softLockupTracing)

	first, err := c.Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	firstValue := first[0].Value

	atomic.AddInt64(&softlockupCounter, 5)

	second, err := c.Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if want := firstValue + 5; second[0].Value != want {
		t.Fatalf("second scrape value = %v, want %v", second[0].Value, want)
	}

	if first[0].Value != firstValue {
		t.Fatalf("earlier scrape was mutated: value = %v, want %v", first[0].Value, firstValue)
	}
}

func TestSoftLockupUpdateConcurrentScrapes(t *testing.T) {
	attr, err := newSoftLockup()
	if err != nil {
		t.Fatalf("newSoftLockup() error = %v", err)
	}
	c := attr.TracingData.(*softLockupTracing)

	const scrapes = 200
	scraped := make(chan []*metric.Data, scrapes)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < scrapes; i++ {
			data, err := c.Update()
			if err != nil {
				t.Errorf("Update() error = %v", err)
				return
			}
			scraped <- data
		}
	}()
	// Simulates prometheusMetric reading Data.Value outside the collector
	// mutex while another scrape updates the counters.
	go func() {
		defer wg.Done()
		for i := 0; i < scrapes; i++ {
			data := <-scraped
			sink = data[0].Value
		}
	}()
	wg.Wait()
}
