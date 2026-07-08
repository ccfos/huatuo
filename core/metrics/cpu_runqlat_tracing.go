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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"sync/atomic"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cpu_runqlat_tracing.c -o $BPF_DIR/cpu_runqlat_tracing.o

type latencyBpfData struct {
	NumVoluntarySwitch   uint64
	NumInVoluntarySwitch uint64
	NumLatencyZone0      uint64
	NumLatencyZone1      uint64
	NumLatencyZone2      uint64
	NumLatencyZone3      uint64
}

type runqlatCollector struct {
	running     atomic.Bool
	bpf         bpf.BPF
	runqlatHost latencyBpfData
}

func init() {
	tracing.RegisterEventTracing("runqlat", newRunqlatCollector)
	_ = pod.RegisterContainerLifeResources("runqlat", reflect.TypeOf(&latencyBpfData{}))
}

func newRunqlatCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &runqlatCollector{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *runqlatCollector) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	if err = b.Attach(); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b.WaitDetachByBreaker(childCtx, cancel)

	c.bpf = b
	c.running.Store(true)

	// wait stop
	<-childCtx.Done()
	c.running.Store(false)
	return nil
}

func aggregatePerCPUValue(raw []byte, dst *latencyBpfData) error {
	chunkSize := binary.Size(latencyBpfData{})
	if chunkSize <= 0 || len(raw)%chunkSize != 0 {
		return fmt.Errorf("unexpected data length %d (chunkSize %d)", len(raw), chunkSize)
	}

	*dst = latencyBpfData{}
	reader := bytes.NewReader(nil)
	for off := 0; off < len(raw); off += chunkSize {
		var cpu latencyBpfData
		reader.Reset(raw[off : off+chunkSize])
		if err := binary.Read(reader, binary.LittleEndian, &cpu); err != nil {
			return err
		}
		dst.NumVoluntarySwitch += cpu.NumVoluntarySwitch
		dst.NumInVoluntarySwitch += cpu.NumInVoluntarySwitch
		dst.NumLatencyZone0 += cpu.NumLatencyZone0
		dst.NumLatencyZone1 += cpu.NumLatencyZone1
		dst.NumLatencyZone2 += cpu.NumLatencyZone2
		dst.NumLatencyZone3 += cpu.NumLatencyZone3
	}
	return nil
}

func (c *runqlatCollector) updateContainerDataCache(cssContainers map[uint64]*pod.Container) error {
	items, err := c.bpf.DumpMapByName("cpu_tg_metric")
	if err != nil {
		return fmt.Errorf("dump bpf map, %w", err)
	}

	var css uint64

	for _, v := range items {
		buf := bytes.NewReader(v.Key)

		if err := binary.Read(buf, binary.LittleEndian, &css); err != nil {
			return fmt.Errorf("read cpu_tg_metric key: %w", err)
		}

		container, ok := cssContainers[css]
		if !ok {
			continue
		}

		cache, ok := container.LifeResources("runqlat").(*latencyBpfData)
		if !ok || cache == nil {
			continue
		}
		if err := aggregatePerCPUValue(v.Value, cache); err != nil {
			return fmt.Errorf("aggregate cpu_tg_metric value: %w", err)
		}
	}

	return nil
}

func (c *runqlatCollector) fetchHostRunqlat() []*metric.Data {
	item, err := c.bpf.ReadMap(c.bpf.MapIDByName("cpu_host_metric"), []byte{0, 0, 0, 0})
	if err != nil || len(item) == 0 {
		return nil
	}

	if err = aggregatePerCPUValue(item, &c.runqlatHost); err != nil {
		return nil
	}

	return []*metric.Data{
		metric.NewGaugeData("latency", float64(c.runqlatHost.NumLatencyZone0), "cpu run queue latency for the host", map[string]string{"zone": "0"}),
		metric.NewGaugeData("latency", float64(c.runqlatHost.NumLatencyZone1), "cpu run queue latency for the host", map[string]string{"zone": "1"}),
		metric.NewGaugeData("latency", float64(c.runqlatHost.NumLatencyZone2), "cpu run queue latency for the host", map[string]string{"zone": "2"}),
		metric.NewGaugeData("latency", float64(c.runqlatHost.NumLatencyZone3), "cpu run queue latency for the host", map[string]string{"zone": "3"}),
	}
}

func (c *runqlatCollector) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal)
	if err != nil {
		return nil, err
	}

	cssContainer := pod.BuildCssContainers(containers, subsystem.SubsystemCPU)

	// update all containers cache data
	if err := c.updateContainerDataCache(cssContainer); err != nil {
		log.Warnf("runqlat: update container cache: %v", err)
	}

	data := []*metric.Data{}
	for _, container := range containers {
		// Skip containers with no CPU cgroup address: they are absent from
		// cssContainer and therefore never updated by updateContainerDataCache.
		// Reporting their zero/stale cache would produce misleading metrics.
		if _, ok := container.CgroupCss[subsystem.SubsystemCPU]; !ok {
			continue
		}

		cache, ok := container.LifeResources("runqlat").(*latencyBpfData)
		if !ok || cache == nil {
			continue
		}

		data = append(data,
			metric.NewContainerGaugeData(container, "latency", float64(cache.NumLatencyZone0), "cpu run queue latency for the containers", map[string]string{"zone": "0"}),
			metric.NewContainerGaugeData(container, "latency", float64(cache.NumLatencyZone1), "cpu run queue latency for the containers", map[string]string{"zone": "1"}),
			metric.NewContainerGaugeData(container, "latency", float64(cache.NumLatencyZone2), "cpu run queue latency for the containers", map[string]string{"zone": "2"}),
			metric.NewContainerGaugeData(container, "latency", float64(cache.NumLatencyZone3), "cpu run queue latency for the containers", map[string]string{"zone": "3"}))
	}

	return append(data, c.fetchHostRunqlat()...), nil
}
