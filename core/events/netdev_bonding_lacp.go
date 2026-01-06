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

package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/vishvananda/netlink"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/netdev_bonding_lacp.c -o $BPF_DIR/netdev_bonding_lacp.o
type lacpTracing struct {
	count uint64
}

func init() {
	// bond mode4 (802.3ad) requires bonding.ko module,
	// the kprobe point is in bonding module, if not exist, should not load bpf
	if !isLacpEnv() {
		return
	}

	tracing.RegisterEventTracing("netdev_bonding_lacp", newLACPTracing)
}

func newLACPTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &lacpTracing{},
		Interval:    60,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (lacp *lacpTracing) Start(ctx context.Context) (err error) {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "ad_event_map", 8192)
	if err != nil {
		return fmt.Errorf("attach and event pipe: %w", err)
	}
	defer reader.Close()

	for {
		select {
		case <-childCtx.Done():
			log.Info("lacp tracing is stopped.")
			return nil
		default:
			var tmp uint64
			if err := reader.ReadInto(&tmp); err != nil {
				return fmt.Errorf("read lacp perf event fail: %w", err)
			}

			atomic.AddUint64(&lacp.count, 1)

			bondInfo, err := readAllFiles("/proc/net/bonding")
			if err != nil {
				log.Warnf("read dir /proc/net/bonding err: %v", err)
				continue
			}

			tracerData := struct {
				Content string `json:"content"`
			}{
				Content: bondInfo,
			}

			log.Debugf("bond info: %s", tracerData.Content)
			storage.Save("lacp", "", time.Now(), tracerData)
		}
	}
}

func (lacp *lacpTracing) Update() ([]*metric.Data, error) {
	return []*metric.Data{
		metric.NewCounterData("total", float64(atomic.LoadUint64(&lacp.count)),
			"lacp disabled count", nil),
	}, nil
}

func readAllFiles(dir string) (string, error) {
	var content string

	return content, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		content += path + "\n" + string(data)
		return nil
	})
}

func isLacpEnv() bool {
	links, err := netlink.LinkList()
	if err != nil {
		return false
	}

	for _, l := range links {
		if l.Type() == "bond" &&
			l.(*netlink.Bond).Mode == netlink.BOND_MODE_802_3AD {
			return true
		}
	}

	return false
}
