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
	"sync/atomic"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("memory_free", newReclaimCompact)
}

func newReclaimCompact() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &reclaimCompact{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_free_compact.c -o $BPF_DIR/memory_free_compact.o

type reclaimCompact struct {
	bpf     bpf.BPF
	running atomic.Bool
}

type memoryLatency struct {
	/* the host latency counters of compaction and alloc pages in direct reclaim. */
	CompactionStall uint64
	AllocPagesStall uint64
	// FIXME: support cgroups v1/v2
}

func (c *reclaimCompact) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	items, err := c.bpf.DumpMapByName("mm_free_compact_map")
	if err != nil {
		return nil, fmt.Errorf("dump map mm_free_compact_map: %w", err)
	}

	var (
		compaction float64
		allocPages float64
	)

	if len(items) != 0 {
		mm := memoryLatency{}
		buf := bytes.NewReader(items[0].Value)
		if err := binary.Read(buf, binary.LittleEndian, &mm); err != nil {
			return nil, err
		}

		compaction = float64(mm.CompactionStall) / 1000 / 1000
		allocPages = float64(mm.AllocPagesStall) / 1000 / 1000
	}

	return []*metric.Data{
		metric.NewGaugeData("compaction_stall", compaction, "time stalled in memory compaction", nil),
		metric.NewGaugeData("allocpages_stall", allocPages, "time stalled in alloc pages", nil),
	}, nil
}

// Start detect work, load bpf and wait data
func (c *reclaimCompact) Start(ctx context.Context) error {
	obj, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer obj.Close()

	if err := obj.Attach(); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	obj.WaitDetachByBreaker(childCtx, cancel)

	c.bpf = obj
	c.running.Store(true)

	// wait stop
	<-childCtx.Done()
	c.running.Store(false)
	return nil
}
