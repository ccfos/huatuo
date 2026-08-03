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
		t.Fatalf("write config file %s: %v", path, err)
	}

	return path
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := writeConfigFile(t, tmpDir, "huatuo-bamai.conf", `
BlackList = ["netdev_hw", "metax_gpu"]

[Log]
Level = "Warn"

[Runtime]
StartupCPULimitCores = 0.75
CPULimitCores = 1.5
MemoryLimitMiB = 2

[HTTPServer]
ListenAddress = "127.0.0.1:29704"
MaxEventStreamClients = 25
EventStreamKeepAliveIntervalSeconds = 15

[Tasks]
MaxConcurrent = 7

[Storage.LocalFile]
Path = "records"
RotationSizeMiB = 64
MaxRotatedFiles = 4

[AutoTracing]
IssuesList = [["dload", "jbd2"]]

[EventTracing]
IssuesList = [["net_rx_latency", "kernel_sched_tick"]]

[EventTracing.NetRxLatency]
ExcludedContainerQos = ["bestEffort"]

[EventTracing.TCPRetransmit]
Filter = "dst port 443"
EnableTLP = true
MaxEventsPerSecond = 42

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
		t.Fatalf("Load returned error: %v", err)
	}

	if len(Get().BlackList) != 2 {
		t.Errorf("unexpected BlackList length: %d", len(Get().BlackList))
	}
	if Get().Log.Level != "Warn" {
		t.Errorf("Log.Level = %q, want %q", Get().Log.Level, "Warn")
	}
	if Get().Runtime.StartupCPULimitCores != 0.75 ||
		Get().Runtime.CPULimitCores != 1.5 {
		t.Errorf("Runtime = %+v, want CPU overrides", Get().Runtime)
	}
	if Get().Runtime.MemoryLimitMiB != 2 {
		t.Errorf("MemoryLimitMiB = %d, want 2", Get().Runtime.MemoryLimitMiB)
	}
	if Get().HTTPServer.ListenAddress != "127.0.0.1:29704" ||
		Get().HTTPServer.MaxEventStreamClients != 25 ||
		Get().HTTPServer.EventStreamKeepAliveIntervalSeconds != 15 {
		t.Errorf("HTTPServer = %+v, want overrides", Get().HTTPServer)
	}
	if Get().Tasks.MaxConcurrent != 7 {
		t.Errorf("Tasks.MaxConcurrent = %d, want 7", Get().Tasks.MaxConcurrent)
	}
	if Get().Storage.LocalFile != (LocalFileConfig{
		Path:            "records",
		RotationSizeMiB: 64,
		MaxRotatedFiles: 4,
	}) {
		t.Errorf("Storage.LocalFile = %+v, want overrides", Get().Storage.LocalFile)
	}
	if len(Get().AutoTracing.IssuesList) != 1 {
		t.Errorf("unexpected AutoTracing.IssuesList length: %d", len(Get().AutoTracing.IssuesList))
	}
	if Get().AutoTracing.CPUSys.IntervalTracing != 1800 {
		t.Errorf(
			"unexpected CPUSys.IntervalTracing: %d",
			Get().AutoTracing.CPUSys.IntervalTracing,
		)
	}
	if len(Get().EventTracing.IssuesList) != 1 {
		t.Errorf("unexpected EventTracing.IssuesList length: %d", len(Get().EventTracing.IssuesList))
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
	if Get().EventTracing.TCPRetransmit.Filter != "dst port 443" {
		t.Errorf("unexpected TCPRetransmit.Filter: %q", Get().EventTracing.TCPRetransmit.Filter)
	}
	if !Get().EventTracing.TCPRetransmit.EnableTLP {
		t.Errorf("TCPRetransmit.EnableTLP should be true")
	}
	if Get().EventTracing.TCPRetransmit.MaxEventsPerSecond != 42 {
		t.Errorf("unexpected TCPRetransmit.MaxEventsPerSecond: %d", Get().EventTracing.TCPRetransmit.MaxEventsPerSecond)
	}
	if Get().Storage.Elasticsearch.Enabled() {
		t.Error("Elasticsearch is enabled without connection settings")
	}
}

func TestLoadRepositoryConfig(t *testing.T) {
	path := filepath.Join("..", "..", "..", "huatuo-bamai.conf")
	if err := Load(path); err != nil {
		t.Fatalf("Load(%q) error = %v", path, err)
	}
}

func TestLoadEnablesCompleteElasticsearchConfig(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", `
[Storage.Elasticsearch]
Address = "http://127.0.0.1:9200"
Username = "elastic"
Password = "secret"
`)

	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !Get().Storage.Elasticsearch.Enabled() {
		t.Fatal("Elasticsearch is disabled with complete connection settings")
	}
	if Get().Storage.Elasticsearch.Index != "huatuo_bamai" {
		t.Fatalf("Elasticsearch index = %q, want default", Get().Storage.Elasticsearch.Index)
	}
}

func TestLoadRejectsPartialElasticsearchConfig(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", `
[Storage.Elasticsearch]
Address = "http://127.0.0.1:9200"
`)

	err := Load(path)
	if err == nil || !strings.Contains(
		err.Error(),
		"address, username, and password must be configured together",
	) {
		t.Fatalf("Load() error = %v, want incomplete Elasticsearch error", err)
	}
}

func TestLoadRejectsLegacyKeys(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "runtime cgroup", contents: "[RuntimeCgroup]\nLimitCPU = 2"},
		{name: "api server", contents: "[APIServer]\nTCPAddr = \":19704\""},
		{name: "task", contents: "[Task]\nMaxRunningTask = 10"},
		{name: "events watch", contents: "[EventsWatch]\nMaxClients = 100"},
		{name: "storage es", contents: "[Storage.ES]\nIndex = \"huatuo_bamai\""},
		{
			name:     "local file rotation size",
			contents: "[Storage.LocalFile]\nRotationSize = 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", tt.contents)
			if err := Load(path); err == nil {
				t.Fatal("Load() error = nil, want strict legacy-key rejection")
			}
		})
	}
}

func TestBamaiConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*BamaiConfig)
		wantErr string
	}{
		{
			name: "invalid log level",
			mutate: func(cfg *BamaiConfig) {
				cfg.Log.Level = "verbose"
			},
			wantErr: "unsupported log level",
		},
		{
			name: "invalid startup cpu limit",
			mutate: func(cfg *BamaiConfig) {
				cfg.Runtime.StartupCPULimitCores = 0
			},
			wantErr: "startup cpu limit",
		},
		{
			name: "invalid listen address",
			mutate: func(cfg *BamaiConfig) {
				cfg.HTTPServer.ListenAddress = "missing-port"
			},
			wantErr: "invalid listen address",
		},
		{
			name: "invalid event stream clients",
			mutate: func(cfg *BamaiConfig) {
				cfg.HTTPServer.MaxEventStreamClients = 0
			},
			wantErr: "maximum event stream clients",
		},
		{
			name: "invalid task concurrency",
			mutate: func(cfg *BamaiConfig) {
				cfg.Tasks.MaxConcurrent = 0
			},
			wantErr: "maximum concurrent tasks",
		},
		{
			name: "invalid local rotation size",
			mutate: func(cfg *BamaiConfig) {
				cfg.Storage.LocalFile.RotationSizeMiB = 0
			},
			wantErr: "local file rotation size",
		},
		{
			name: "invalid kubelet port",
			mutate: func(cfg *BamaiConfig) {
				cfg.Pod.KubeletReadOnlyPort = 65536
			},
			wantErr: "kubelet read-only port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadConfigDefaults(t)
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want contain %q", err, tt.wantErr)
			}
		})
	}
}

func loadConfigDefaults(t *testing.T) *BamaiConfig {
	t.Helper()
	path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", "")
	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loaded := *Get()
	return &loaded
}

func TestSetAndSync(t *testing.T) {
	tmpDir := t.TempDir()
	path := writeConfigFile(t, tmpDir, "huatuo-bamai.conf", `
BlackList = ["netdev_hw"]

[AutoTracing]
IssuesList = [["dload", "jbd2"]]

[EventTracing]
IssuesList = [["net_rx_latency", "kernel_sched_tick"]]

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
		t.Fatalf("Load returned error: %v", err)
	}

	for _, kv := range []struct {
		key string
		val any
	}{
		{"BlackList", []string{"netdev_hw", "metax_gpu"}},
		{"AutoTracing.IssuesList", [][]string{{"cpuidle", "perf"}}},
		{"EventTracing.IssuesList", [][]string{{"dropwatch", "kfree_skb"}}},
		{"MetricCollector.Vmstat.IncludedOnHost", "pgsteal_direct"},
		{"MetricCollector.Vmstat.IncludedOnContainer", "workingset_refault_file"},
	} {
		if err := Set(kv.key, kv.val); err != nil {
			t.Fatalf("Set %s returned error: %v", kv.key, err)
		}
	}

	if err := Sync(); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	if err := Load(path); err != nil {
		t.Fatalf("Load after Sync returned error: %v", err)
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
	if len(Get().AutoTracing.IssuesList) != 1 || len(Get().AutoTracing.IssuesList[0]) != 2 || Get().AutoTracing.IssuesList[0][0] != "cpuidle" {
		t.Errorf("unexpected AutoTracing.IssuesList after reload: %#v", Get().AutoTracing.IssuesList)
	}
	if len(Get().EventTracing.IssuesList) != 1 || len(Get().EventTracing.IssuesList[0]) != 2 || Get().EventTracing.IssuesList[0][0] != "dropwatch" {
		t.Errorf("unexpected EventTracing.IssuesList after reload: %#v", Get().EventTracing.IssuesList)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read synced config: %v", err)
	}
	if !strings.Contains(string(raw), "[AutoTracing]") || !strings.Contains(string(raw), "IssuesList = [[\"cpuidle\", \"perf\"]]") {
		t.Errorf("synced config should persist AutoTracing.IssuesList, got %s", string(raw))
	}
	if !strings.Contains(string(raw), "[EventTracing]") || !strings.Contains(string(raw), "IssuesList = [[\"dropwatch\", \"kfree_skb\"]]") {
		t.Errorf("synced config should persist EventTracing.IssuesList, got %s", string(raw))
	}
	if !strings.Contains(string(raw), "[MetricCollector.Vmstat]") || !strings.Contains(string(raw), "IncludedOnContainer = \"workingset_refault_file\"") {
		t.Errorf("synced config should persist MetricCollector.Vmstat.IncludedOnContainer, got %s", string(raw))
	}
	if !strings.Contains(string(raw), "MemoryLimitMiB = 2048") {
		t.Errorf("synced config should preserve the public memory unit, got %s", string(raw))
	}
}
