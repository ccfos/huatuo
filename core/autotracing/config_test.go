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

package autotracing

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
	src := &Config{IssuesList: [][]string{{"dload", "jbd2"}}}
	Set(src)
	src.IssuesList[0][0] = "cpuidle"

	if got := configSnapshot().IssuesList[0][0]; got != "dload" {
		t.Fatalf("IssuesList[0][0] = %q, want detached value", got)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	testConcurrentSnapshots(t, [][2]int64{{3, 300}, {4, 400}})
}

func testConcurrentSnapshots(t *testing.T, pairs [][2]int64) {
	t.Helper()
	Set(&Config{})
	valid := map[[2]int64]bool{{0, 0}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair [2]int64) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.CPUSys.Interval = pair[0]
				cfg.CPUSys.IntervalTracing = pair[1]
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
				got := [2]int64{cfg.CPUSys.Interval, cfg.CPUSys.IntervalTracing}
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
