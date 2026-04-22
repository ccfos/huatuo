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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Errorf("write config file %s: %v", path, err)
		return ""
	}

	return path
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := writeConfigFile(t, tmpDir, "huatuo-bamai.conf", `
BlackList = ["netdev_hw", "metax_gpu"]

[RuntimeCgroup]
LimitMem = 2

[AutoTracing]
PatternList = [["dload", "jbd2"]]

[EventTracing]
PatternList = [["net_rx_latency", "kernel_sched_tick"]]

[EventTracing.NetRxLatency]
ExcludedContainerQos = ["bestEffort"]

[MetricCollector.Vmstat]
IncludedOnHost = "pgscan_direct"
ExcludedOnHost = "total"
IncludedOnContainer = "inactive_file"
ExcludedOnContainer = "writeback"
`)
	if path == "" {
		return
	}

	if err := Load(path); err != nil {
		t.Errorf("Load returned error: %v", err)
		return
	}

	if len(Get().BlackList) != 2 {
		t.Errorf("unexpected BlackList length: %d", len(Get().BlackList))
	}
	if Get().RuntimeCgroup.LimitMem != 2*1024*1024 {
		t.Errorf("LimitMem should be converted to bytes, got %d", Get().RuntimeCgroup.LimitMem)
	}
	if len(Get().AutoTracing.PatternList) != 1 {
		t.Errorf("unexpected AutoTracing.PatternList length: %d", len(Get().AutoTracing.PatternList))
	}
	if len(Get().EventTracing.PatternList) != 1 {
		t.Errorf("unexpected EventTracing.PatternList length: %d", len(Get().EventTracing.PatternList))
	}
	if Get().MetricCollector.Vmstat.IncludedOnHost != "pgscan_direct" {
		t.Errorf("unexpected Vmstat.IncludedOnHost: %q", Get().MetricCollector.Vmstat.IncludedOnHost)
	}
	if Get().MetricCollector.Vmstat.IncludedOnContainer != "inactive_file" {
		t.Errorf("unexpected Vmstat.IncludedOnContainer: %q", Get().MetricCollector.Vmstat.IncludedOnContainer)
	}
	if len(Get().EventTracing.NetRxLatency.ExcludedContainerQos) != 1 {
		t.Errorf("unexpected ExcludedContainerQos length: %d", len(Get().EventTracing.NetRxLatency.ExcludedContainerQos))
	}
}

func TestSetAndSync(t *testing.T) {
	tmpDir := t.TempDir()
	path := writeConfigFile(t, tmpDir, "huatuo-bamai.conf", `
BlackList = ["netdev_hw"]

[AutoTracing]
PatternList = [["dload", "jbd2"]]

[EventTracing]
PatternList = [["net_rx_latency", "kernel_sched_tick"]]

[MetricCollector.Vmstat]
IncludedOnHost = "pgscan_direct"
ExcludedOnHost = "total"
IncludedOnContainer = "inactive_file"
ExcludedOnContainer = "writeback"
`)
	if path == "" {
		return
	}

	if err := Load(path); err != nil {
		t.Errorf("Load returned error: %v", err)
		return
	}

	Set("Blacklist", []string{"netdev_hw", "metax_gpu"})
	Set("AutoTracing.PatternList", [][]string{{"cpuidle", "perf"}})
	Set("EventTracing.PatternList", [][]string{{"dropwatch", "kfree_skb"}})
	Set("MetricCollector.Vmstat.IncludedOnHost", "pgsteal_direct")
	Set("MetricCollector.Vmstat.IncludedOnContainer", "workingset_refault_file")

	if err := Sync(); err != nil {
		t.Errorf("Sync returned error: %v", err)
		return
	}

	if err := Load(path); err != nil {
		t.Errorf("Load after Sync returned error: %v", err)
		return
	}

	if len(Get().BlackList) != 2 {
		t.Errorf("unexpected BlackList length after reload: %d", len(Get().BlackList))
	}
	if Get().MetricCollector.Vmstat.IncludedOnHost != "pgsteal_direct" {
		t.Errorf("unexpected Vmstat.IncludedOnHost after reload: %q", Get().MetricCollector.Vmstat.IncludedOnHost)
	}
	if Get().MetricCollector.Vmstat.IncludedOnContainer != "workingset_refault_file" {
		t.Errorf("unexpected Vmstat.IncludedOnContainer after reload: %q", Get().MetricCollector.Vmstat.IncludedOnContainer)
	}
	if len(Get().AutoTracing.PatternList) != 1 || len(Get().AutoTracing.PatternList[0]) != 2 || Get().AutoTracing.PatternList[0][0] != "cpuidle" {
		t.Errorf("unexpected AutoTracing.PatternList after reload: %#v", Get().AutoTracing.PatternList)
	}
	if len(Get().EventTracing.PatternList) != 1 || len(Get().EventTracing.PatternList[0]) != 2 || Get().EventTracing.PatternList[0][0] != "dropwatch" {
		t.Errorf("unexpected EventTracing.PatternList after reload: %#v", Get().EventTracing.PatternList)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read synced config: %v", err)
		return
	}
	if !strings.Contains(string(raw), "[AutoTracing]") || !strings.Contains(string(raw), "PatternList = [[\"cpuidle\", \"perf\"]]") {
		t.Errorf("synced config should persist AutoTracing.PatternList, got %s", string(raw))
	}
	if !strings.Contains(string(raw), "[EventTracing]") || !strings.Contains(string(raw), "PatternList = [[\"dropwatch\", \"kfree_skb\"]]") {
		t.Errorf("synced config should persist EventTracing.PatternList, got %s", string(raw))
	}
	if !strings.Contains(string(raw), "[MetricCollector.Vmstat]") || !strings.Contains(string(raw), "IncludedOnContainer = \"workingset_refault_file\"") {
		t.Errorf("synced config should persist MetricCollector.Vmstat.IncludedOnContainer, got %s", string(raw))
	}
}
