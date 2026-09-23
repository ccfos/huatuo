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
	"strings"
	"sync"
	"testing"

	testutils "github.com/ccfos/huatuo/internal/testing"
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

func TestConfigValidateRegexFields(t *testing.T) {
	fields := []struct {
		path string
		set  func(*Config, string)
	}{
		{"NetdevStats.DeviceIncluded", func(c *Config, v string) { c.NetdevStats.DeviceIncluded = v }},
		{"NetdevStats.DeviceExcluded", func(c *Config, v string) { c.NetdevStats.DeviceExcluded = v }},
		{"Qdisc.DeviceIncluded", func(c *Config, v string) { c.Qdisc.DeviceIncluded = v }},
		{"Qdisc.DeviceExcluded", func(c *Config, v string) { c.Qdisc.DeviceExcluded = v }},
		{"Vmstat.IncludedOnHost", func(c *Config, v string) { c.Vmstat.IncludedOnHost = v }},
		{"Vmstat.ExcludedOnHost", func(c *Config, v string) { c.Vmstat.ExcludedOnHost = v }},
		{"Vmstat.IncludedOnContainer", func(c *Config, v string) { c.Vmstat.IncludedOnContainer = v }},
		{"Vmstat.ExcludedOnContainer", func(c *Config, v string) { c.Vmstat.ExcludedOnContainer = v }},
		{"MemoryEvents.Included", func(c *Config, v string) { c.MemoryEvents.Included = v }},
		{"MemoryEvents.Excluded", func(c *Config, v string) { c.MemoryEvents.Excluded = v }},
		{"Netstat.Included", func(c *Config, v string) { c.Netstat.Included = v }},
		{"Netstat.Excluded", func(c *Config, v string) { c.Netstat.Excluded = v }},
		{"MountPointStat.MountPointsIncluded", func(c *Config, v string) { c.MountPointStat.MountPointsIncluded = v }},
	}

	for _, f := range fields {
		t.Run(f.path, func(t *testing.T) {
			c := &Config{}
			if err := c.Validate(); err != nil {
				t.Fatalf("empty pattern: Validate() = %v, want nil", err)
			}

			f.set(c, `^(eth0|eth1)$`)
			if err := c.Validate(); err != nil {
				t.Fatalf("valid pattern: Validate() = %v, want nil", err)
			}

			f.set(c, "[")
			err := c.Validate()
			if err == nil {
				t.Fatal(`invalid pattern "[": Validate() = nil, want error`)
			}
			if !strings.Contains(err.Error(), f.path) {
				t.Fatalf("error %q does not name field %q", err, f.path)
			}
		})
	}
}
