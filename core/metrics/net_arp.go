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

package collector

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/utils/timeutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

const arpCacheInterval = 60 * time.Second

type arpContainerRaw struct {
	container *pod.Container
	entries   int64
}

type arpHostRaw struct {
	entries      int64
	cacheEntries uint64
}

type arpCollector struct {
	mu           sync.RWMutex
	arpCtrCache  []arpContainerRaw
	arpHostCache *arpHostRaw
	running      atomic.Bool
}

func init() {
	tracing.RegisterEventTracing("arp", newArp)
}

func newArp() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &arpCollector{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *arpCollector) Start(ctx context.Context) error {
	c.running.Store(true)
	defer c.running.Store(false)

	timeutil.RunEvery(ctx, arpCacheInterval, c.readAndCache)
	return nil
}

func (c *arpCollector) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	c.mu.RLock()
	arpCtrCache := c.arpCtrCache
	arpHostCache := c.arpHostCache
	c.mu.RUnlock()

	return buildArpMetrics(arpCtrCache, arpHostCache), nil
}

func (c *arpCollector) readAndCache() {
	containers, err := pod.NormalContainers()
	if err != nil {
		log.Warnf("arp: list containers failed: %v", err)
		return
	}

	arpCtrCache := make([]arpContainerRaw, 0, len(containers))
	for _, container := range containers {
		count, err := CountLines(procfs.Path(strconv.Itoa(container.InitPid), "net/arp"))
		if err != nil {
			log.Debugf("arp metrics for container %v: %v", container, err)
			continue
		}
		arpCtrCache = append(arpCtrCache, arpContainerRaw{container: container, entries: count - 1})
	}

	var hostEntries int64
	if count, err := CountLines(procfs.Path("1/net/arp")); err == nil && count > 1 {
		hostEntries = count - 1
	}
	var cacheEntries uint64
	if cache, err := procfs.NetArpCache(); err == nil {
		cacheEntries = cache.Stats["entries"]
	}

	c.mu.Lock()
	c.arpCtrCache = arpCtrCache
	c.arpHostCache = &arpHostRaw{
		entries:      hostEntries,
		cacheEntries: cacheEntries,
	}
	c.mu.Unlock()
}

func buildArpMetrics(arpCtrCache []arpContainerRaw, arpHostCache *arpHostRaw) []*metric.Data {
	var data []*metric.Data
	for _, cd := range arpCtrCache {
		data = append(data, metric.NewContainerGaugeData(cd.container, "entries",
			float64(cd.entries), "arp entries in container netns", nil))
	}
	if arpHostCache != nil {
		data = append(
			data,
			metric.NewGaugeData("entries", float64(arpHostCache.entries),
				"arp entries in host init network namespace", nil),
			metric.NewGaugeData("total", float64(arpHostCache.cacheEntries),
				"all entries in arp_cache for containers and host netns", nil),
		)
	}
	return data
}
