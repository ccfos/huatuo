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

package collector

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
	src := &Config{}
	src.NetdevDCB.DeviceList = []string{"eth0"}
	Set(src)
	src.NetdevDCB.DeviceList[0] = "eth1"

	if got := configSnapshot().NetdevDCB.DeviceList[0]; got != "eth0" {
		t.Fatalf("NetdevDCB.DeviceList[0] = %q, want detached value", got)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	type filters struct {
		included string
		excluded string
	}
	pairs := []filters{{"eth0", "lo"}, {"eth1", "docker0"}}
	Set(&Config{})
	valid := map[filters]bool{{}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair filters) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.NetdevStats.DeviceIncluded = pair.included
				cfg.NetdevStats.DeviceExcluded = pair.excluded
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
				got := filters{
					included: cfg.NetdevStats.DeviceIncluded,
					excluded: cfg.NetdevStats.DeviceExcluded,
				}
				if !valid[got] {
					select {
					case errCh <- fmt.Errorf("observed mixed config snapshot: %+v", got):
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
