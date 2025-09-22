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

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("memory_free", newMemoryHost)
}

func newMemoryHost() (*tracing.EventTracingAttr, error) {
	mm := &memoryHost{
		metrics: []*metric.Data{
			metric.NewGaugeData("compaction", 0, "time elapsed in memory compaction", nil),
			metric.NewGaugeData("allocstall", 0, "time elapsed in memory allocstall", nil),
		},
	}
	return &tracing.EventTracingAttr{
		TracingData: mm,
		Internal:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_free_compact.c -o $BPF_DIR/memory_free_compact.o

type memoryHost struct {
	metrics  []*metric.Data
	bpf      bpf.BPF
	isRuning bool
}

type memoryHostMetric struct {
	/* host: compaction latency */
	CompactionStat uint64
	/* host: page alloc latency in direct reclaim */
	AllocstallStat uint64
}

func (c *memoryHost) Update() ([]*metric.Data, error) {
	if !c.isRuning {
		return nil, nil
	}

	items, err := c.bpf.DumpMapByName("mm_free_compact_map")
	if err != nil {
		return nil, fmt.Errorf("dump map mm_free_compact_map: %w", err)
	}

	if len(items) == 0 {
		c.metrics[0].Value = float64(0)
		c.metrics[1].Value = float64(0)
	} else {
		mmMetric := memoryHostMetric{}
		buf := bytes.NewReader(items[0].Value)
		err := binary.Read(buf, binary.LittleEndian, &mmMetric)
		if err != nil {
			return nil, fmt.Errorf("read mem_cgroup_map: %w", err)
		}
		c.metrics[0].Value = float64(mmMetric.CompactionStat) / 1000 / 1000
		c.metrics[1].Value = float64(mmMetric.AllocstallStat) / 1000 / 1000
	}
	return c.metrics, nil
}

// Start detect work, load bpf and wait data form perfevent
func (c *memoryHost) Start(ctx context.Context) error {
	var err error
	c.bpf, err = bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer c.bpf.Close()

	if err = c.bpf.Attach(); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.bpf.WaitDetachByBreaker(childCtx, cancel)

	c.isRuning = true
	<-childCtx.Done()
	c.isRuning = false
	return nil
}
