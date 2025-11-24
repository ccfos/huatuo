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
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/runqlat_tracing.c -o $BPF_DIR/runqlat_tracing.o

type latencyBpfData struct {
	NumVoluntarySwitch   uint64
	NumInVoluntarySwitch uint64
	NumLatency01         uint64
	NumLatency02         uint64
	NumLatency03         uint64
	NumLatency04         uint64
}

var (
	globalRunqlat  latencyBpfData
	runqlatRunning bool
)

func startRunqlatTracerWork(ctx context.Context) error {
	// load bpf.
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	if err = b.Attach(); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b.WaitDetachByBreaker(childCtx, cancel)

	runqlatRunning = true

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			var css uint64

			containers, err := pod.GetNormalContainers()
			if err != nil {
				return fmt.Errorf("get normal containers: %w", err)
			}
			cssToContainer := pod.BuildCSSToContainer(containers, "cpu")

			items, err := b.DumpMapByName("cpu_tg_metric")
			if err != nil {
				return fmt.Errorf("failed to dump cpu_tg_metric: %w", err)
			}
			for _, v := range items {
				buf := bytes.NewReader(v.Key)
				if err = binary.Read(buf, binary.LittleEndian, &css); err != nil {
					return fmt.Errorf("can't read cpu_tg_metric key: %w", err)
				}
				container := cssToContainer[css]
				if container == nil {
					continue
				}

				buf = bytes.NewReader(v.Value)
				if err = binary.Read(buf, binary.LittleEndian, container.LifeResouces("runqlat").(*latencyBpfData)); err != nil {
					return fmt.Errorf("can't read cpu_tg_metric value: %w", err)
				}
			}

			item, err := b.ReadMap(b.MapIDByName("cpu_host_metric"), []byte{0, 0, 0, 0})
			if err != nil {
				return fmt.Errorf("failed to read cpu_host_metric: %w", err)
			}
			buf := bytes.NewReader(item)
			if err = binary.Read(buf, binary.LittleEndian, &globalRunqlat); err != nil {
				log.Errorf("can't read cpu_host_metric: %v", err)
				return err
			}

			time.Sleep(2 * time.Second)
		}
	}
}

// Start runqlat work, load bpf and wait data form perfevent
func (c *runqlatCollector) Start(ctx context.Context) error {
	err := startRunqlatTracerWork(ctx)

	containers, _ := pod.GetContainersByType(pod.ContainerTypeNormal)
	for _, container := range containers {
		runqlatData := container.LifeResouces("runqlat").(*latencyBpfData)
		*runqlatData = latencyBpfData{}
	}

	runqlatRunning = false

	return err
}
