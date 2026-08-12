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
	"fmt"
	"sync"
	"testing"

	testutils "huatuo-bamai/internal/testing"
)

func TestConfigCloneDoesNotShareMutableReferences(t *testing.T) {
	source := &Config{}
	testutils.PopulateCloneSource(t, source)

	testutils.AssertDeepClone(t, source, source.Clone())
}

func TestSetPublishesIndependentConfig(t *testing.T) {
	src := &Config{IssuesList: [][]string{{"dropwatch", "kfree_skb"}}}
	src.Netdev.DeviceList = []string{"eth0"}
	Set(src)
	src.IssuesList[0][0] = "net_rx_latency"
	src.Netdev.DeviceList[0] = "eth1"

	snapshot := configSnapshot()
	if snapshot.IssuesList[0][0] != "dropwatch" || snapshot.Netdev.DeviceList[0] != "eth0" {
		t.Fatalf("published config aliases caller data: %+v", snapshot)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	pairs := [][2]uint64{{3, 300}, {4, 400}}
	Set(&Config{})
	valid := map[[2]uint64]bool{{0, 0}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair [2]uint64) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.NetRxLatency.Driver2NetRx = pair[0]
				cfg.NetRxLatency.Driver2TCP = pair[1]
				Set(cfg)
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				cfg := configSnapshot()
				got := [2]uint64{cfg.NetRxLatency.Driver2NetRx, cfg.NetRxLatency.Driver2TCP}
				if !valid[got] {
					select {
					case errCh <- fmt.Errorf("observed mixed config snapshot: %v", got):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
