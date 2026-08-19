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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	testutils "huatuo-bamai/internal/testing"
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

[EventTracing.SchedTick]
IntervalThreshold = 20000000

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
	if Get().EventTracing.SchedTick.IntervalThreshold != 20000000 {
		t.Errorf(
			"EventTracing.SchedTick.IntervalThreshold = %d, want 20000000",
			Get().EventTracing.SchedTick.IntervalThreshold,
		)
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

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "invalid log level",
			mutate: func(cfg *Config) {
				cfg.Log.Level = "verbose"
			},
			wantErr: "unsupported log level",
		},
		{
			name: "invalid startup cpu limit",
			mutate: func(cfg *Config) {
				cfg.Runtime.StartupCPULimitCores = 0
			},
			wantErr: "startup cpu limit",
		},
		{
			name: "invalid listen address",
			mutate: func(cfg *Config) {
				cfg.HTTPServer.ListenAddress = "missing-port"
			},
			wantErr: "invalid listen address",
		},
		{
			name: "invalid event stream clients",
			mutate: func(cfg *Config) {
				cfg.HTTPServer.MaxEventStreamClients = 0
			},
			wantErr: "maximum event stream clients",
		},
		{
			name: "invalid task concurrency",
			mutate: func(cfg *Config) {
				cfg.Tasks.MaxConcurrent = 0
			},
			wantErr: "maximum concurrent tasks",
		},
		{
			name: "invalid local rotation size",
			mutate: func(cfg *Config) {
				cfg.Storage.LocalFile.RotationSizeMiB = 0
			},
			wantErr: "local file rotation size",
		},
		{
			name: "invalid kubelet port",
			mutate: func(cfg *Config) {
				cfg.Pod.KubeletReadOnlyPort = 65536
			},
			wantErr: "kubelet read-only port",
		},
		{
			name: "invalid autotracing issue expression",
			mutate: func(cfg *Config) {
				cfg.AutoTracing.IssuesList = [][]string{{"broken", "["}}
			},
			wantErr: "validating autotracing issues list",
		},
		{
			name: "invalid scheduler tick threshold",
			mutate: func(cfg *Config) {
				cfg.EventTracing.SchedTick.IntervalThreshold = 0
			},
			wantErr: "validating event tracing config: scheduler tick interval threshold",
		},
		{
			name: "invalid event tracing issue shape",
			mutate: func(cfg *Config) {
				cfg.EventTracing.IssuesList = [][]string{{"missing-expression"}}
			},
			wantErr: "validating event tracing config: validating issues list",
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

func TestLoadRejectsInvalidIssuesListExpression(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", `
[EventTracing]
IssuesList = [["broken", "["]]
`)
	err := Load(path)
	if err == nil || !strings.Contains(err.Error(),
		`rule 0 "broken" has invalid regular expression "["`) {
		t.Fatalf("Load() error = %v, want actionable expression error", err)
	}
}

func loadConfigDefaults(t *testing.T) *Config {
	t.Helper()
	path := writeConfigFile(t, t.TempDir(), "huatuo-bamai.conf", "")
	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return Get().Clone()
}

func TestConfigCloneDoesNotShareMutableReferences(t *testing.T) {
	source := &Config{}
	testutils.PopulateCloneSource(t, source)

	testutils.AssertDeepClone(t, source, source.Clone())
}

func TestUpdateAndSync(t *testing.T) {
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

	if err := UpdateAndSync(map[string]any{
		"BlackList":                                  []string{"netdev_hw", "metax_gpu"},
		"AutoTracing.IssuesList":                     [][]string{{"cpuidle", "perf"}},
		"EventTracing.IssuesList":                    [][]string{{"dropwatch", "kfree_skb"}},
		"MetricCollector.Vmstat.IncludedOnHost":      "pgsteal_direct",
		"MetricCollector.Vmstat.IncludedOnContainer": "workingset_refault_file",
	}); err != nil {
		t.Fatalf("UpdateAndSync returned error: %v", err)
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

func TestUpdateRollsBackInvalidBatch(t *testing.T) {
	loadConfigDefaults(t)
	before := Get()

	err := Update(map[string]any{
		"BlackList": []string{"dropwatch"},
		"NotExist":  1,
	})
	if !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("Update() error = %v, want ErrInvalidUpdate", err)
	}
	if Get() != before {
		t.Fatal("Update() published a partial config")
	}
}

func TestUpdateRejectsOverlappingKeys(t *testing.T) {
	loadConfigDefaults(t)

	err := Update(map[string]any{
		"Runtime":                Get().Runtime,
		"Runtime.MemoryLimitMiB": int64(1024),
	})
	if !errors.Is(err, ErrInvalidUpdate) || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("Update() error = %v, want overlapping-fields error", err)
	}
}

func TestUpdateDetachesCallerValues(t *testing.T) {
	loadConfigDefaults(t)
	blacklist := []string{"dropwatch"}

	if err := Update(map[string]any{"BlackList": blacklist}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	blacklist[0] = "netdev_hw"

	if got := Get().BlackList[0]; got != "dropwatch" {
		t.Fatalf("BlackList[0] = %q, want detached value", got)
	}
}

func TestUpdatePublishesConsistentSnapshots(t *testing.T) {
	loadConfigDefaults(t)

	type limits struct {
		cpu    float64
		memory int64
	}
	pairs := []limits{{cpu: 3, memory: 300}, {cpu: 4, memory: 400}}
	valid := map[limits]bool{
		{cpu: Get().Runtime.CPULimitCores, memory: Get().Runtime.MemoryLimitMiB}: true,
		pairs[0]: true,
		pairs[1]: true,
	}

	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for _, pair := range pairs {
		wg.Add(1)
		go func(pair limits) {
			defer wg.Done()
			<-start
			for range 200 {
				if err := Update(map[string]any{
					"Runtime.CPULimitCores":  pair.cpu,
					"Runtime.MemoryLimitMiB": pair.memory,
				}); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				snapshot := Get()
				got := limits{
					cpu:    snapshot.Runtime.CPULimitCores,
					memory: snapshot.Runtime.MemoryLimitMiB,
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
