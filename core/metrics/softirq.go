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
	"strconv"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/tklauser/numcpus"
)

func init() {
	tracing.RegisterEventTracing("softirq", newSoftirq)
}

func newSoftirq() (*tracing.EventTracingAttr, error) {
	cpuPossible, err := numcpus.GetPossible()
	if err != nil {
		return nil, fmt.Errorf("fetch possible cpu num")
	}

	cpuOnline, err := numcpus.GetOnline()
	if err != nil {
		return nil, fmt.Errorf("fetch possible cpu num")
	}

	return &tracing.EventTracingAttr{
		TracingData: &softirqLatency{
			bpf:         nil,
			isRunning:   false,
			cpuPossible: cpuPossible,
			cpuOnline:   cpuOnline,
		},
		Internal: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/softirq.c -o $BPF_DIR/softirq.o

type softirqLatency struct {
	bpf         bpf.BPF
	isRunning   bool
	cpuPossible int
	cpuOnline   int
}

type softirqLatencyData struct {
	Timestamp    uint64
	TotalLatency [4]uint64
}

const (
	softirqHi = iota
	softirqTime
	softirqNetTx
	softirqNetRx
	softirqBlock
	softirqIrqPoll
	softirqTasklet
	softirqSched
	softirqHrtimer
	sofirqRcu
	softirqMax
)

func irqTypeName(id int) string {
	switch id {
	case softirqHi:
		return "HI"
	case softirqTime:
		return "TIMER"
	case softirqNetTx:
		return "NET_TX"
	case softirqNetRx:
		return "NET_RX"
	case softirqBlock:
		return "BLOCK"
	case softirqIrqPoll:
		return "IRQ_POLL"
	case softirqTasklet:
		return "TASKLET"
	case softirqSched:
		return "SCHED"
	case softirqHrtimer:
		return "HRTIMER"
	case sofirqRcu:
		return "RCU"
	default:
		return "ERR_TYPE"
	}
}

func irqAllowed(id int) bool {
	switch id {
	case softirqNetTx, softirqNetRx:
		return true
	default:
		return false
	}
}

func (s *softirqLatency) Update() ([]*metric.Data, error) {
	if !s.isRunning {
		return nil, nil
	}

	items, err := s.bpf.DumpMapByName("softirq_percpu_lats")
	if err != nil {
		return nil, fmt.Errorf("dump map: %w", err)
	}

	labels := make(map[string]string)
	metricData := []*metric.Data{}

	// IRQ: 0 ... NR_SOFTIRQS_MAX
	for _, item := range items {
		var irqVector uint32
		latencyOnAllCPU := make([]softirqLatencyData, s.cpuPossible)

		if err = binary.Read(bytes.NewReader(item.Key), binary.LittleEndian, &irqVector); err != nil {
			return nil, fmt.Errorf("read map key: %w", err)
		}

		if !irqAllowed(int(irqVector)) {
			continue
		}

		if err = binary.Read(bytes.NewReader(item.Value), binary.LittleEndian, &latencyOnAllCPU); err != nil {
			return nil, fmt.Errorf("read map value: %w", err)
		}

		labels["type"] = irqTypeName(int(irqVector))

		for cpuid, lat := range latencyOnAllCPU {
			if cpuid >= s.cpuOnline {
				break
			}
			labels["cpuid"] = strconv.Itoa(cpuid)
			for zoneid, zone := range lat.TotalLatency {
				labels["zone"] = strconv.Itoa(zoneid)
				metricData = append(metricData, metric.NewGaugeData("latency", float64(zone), "softirq latency", labels))
			}
		}
	}

	return metricData, nil
}

func (s *softirqLatency) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	if err = b.Attach(); err != nil {
		return err
	}

	s.bpf = b
	s.isRunning = true

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b.WaitDetachByBreaker(childCtx, cancel)

	<-childCtx.Done()

	s.isRunning = false
	return nil
}
