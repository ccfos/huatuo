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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

// Block discovery so cancellation cannot race past the cleanup assertion.
type blockingMemoryCgroup struct {
	cgroups.Cgroup
	entered chan struct{}
	release chan struct{}
}

func (c *blockingMemoryCgroup) MemoryUsage(string) (*stats.MemoryUsage, error) {
	close(c.entered)
	<-c.release
	return &stats.MemoryUsage{MaxLimited: 1 << 20}, nil
}

func TestNewMemoryThresholdSnapshot(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	Set(&Config{})

	attr, err := newMemoryThresholdSnapshot()
	if err != nil {
		t.Fatalf("newMemoryThresholdSnapshot() error = %v", err)
	}
	if attr == nil || attr.TracingData == nil {
		t.Fatal("memory threshold snapshots require an explicit enablement setting")
	}
}

func TestMemoryThresholdSnapshotBlacklist(t *testing.T) {
	// Disable all autotracers so this test only exercises registration.
	blacklist := []string{"cpuidle", "cpusys", "dload", "iotracing", "memburst", "memory_threshold_snapshot"}
	registered, err := tracing.NewRegister(blacklist)
	if err != nil {
		t.Fatalf("initialize blacklisted autotracers: %v", err)
	}
	if len(registered) != 0 {
		t.Fatalf("registered autotracers = %v, want none", registered)
	}
	status := tracing.EventTracingStatus()
	if got := status["memory_threshold_snapshot"]; got != "disabled" {
		t.Errorf("memory_threshold_snapshot status = %q, want disabled", got)
	}
	if _, ok := status["before_oom_memsnap"]; ok {
		t.Error("legacy before_oom_memsnap tracer is still registered")
	}
}

func TestMemoryThresholdSnapshotStopJoinsWatcherBeforeRestart(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.limit_in_bytes", "memory.usage_in_bytes", "cgroup.event_control"} {
		writeMemoryEventsForTest(t, filepath.Join(directory, name), "0")
	}
	cfg := &Config{}
	cfg.MemoryThresholdSnapshot.ThresholdPercent = 90
	snapshot := &memoryThresholdSnapshot{}
	for iteration := 0; iteration < 2; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			backend := &blockingMemoryCgroup{entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(backend.release) })
			watcher, err := openPressureWatcher(
				backend,
				cfg.MemoryThresholdSnapshot.ThresholdPercent,
				cgroups.Legacy,
				root,
			)
			if err != nil {
				t.Fatal(err)
			}
			// Ordinary files exercise v1 FD registration without changing host cgroups.
			watcher.root, watcher.mode = root, cgroups.Legacy
			fds := []int{watcher.epollFD, watcher.inotifyFD, watcher.controlFD}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- snapshot.watchAndCapture(ctx, cfg, watcher)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("watcher did not stop")
				}
			})
			select {
			case <-backend.entered:
			case <-time.After(time.Second):
				t.Fatal("watcher did not start discovery")
			}
			cancel()
			select {
			case err := <-done:
				t.Fatalf("tracer returned before watcher cleanup: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("tracer did not stop after releasing discovery")
			}
			control, err := os.ReadFile(filepath.Join(directory, "cgroup.event_control"))
			if err != nil {
				t.Fatal(err)
			}
			var pressureFD int
			if _, err := fmt.Sscanf(string(control), "%d", &pressureFD); err != nil {
				t.Fatal(err)
			}
			for _, fd := range append(fds, pressureFD) {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Errorf("FD %d remains open after stop: %v", fd, err)
				}
			}
		})
	}
}

func TestMemoryThresholdSnapshotRevalidatesContainerBeforePersistence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			outputDir := t.TempDir()
			store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{
				LocalFile: &tracingstore.LocalFileConfig{
					Path: outputDir, RotationSizeMiB: 1, MaxRotatedFiles: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(t.Context()); err != nil {
					t.Error(err)
				}
			})
			if err := tracing.EnableDocumentWriter(store, document.New("test")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tracing.DisableDocumentWriter)

			path, saved := "/original", false
			identity := memsnapshot.ProcessIdentity{TGID: 42, StartTimeTicks: 100}
			result := &collector.Result{
				Identity: identity, Language: memsnapshot.LanguageGo,
				Snapshot:      &memsnapshot.Snapshot{Status: memsnapshot.StatusComplete},
				ProcessMemory: &memsnapshot.ProcessMemory{Status: memsnapshot.StatusComplete},
			}
			ops := &memoryThresholdSnapshotOps{
				selectTarget: func(context.Context, string, uint64) (targetCandidate, error) {
					return targetCandidate{pid: 42, identity: identity}, nil
				},
				validate: func(_ context.Context, path string, actual memsnapshot.ProcessIdentity) error {
					if path != "/original" || actual != identity {
						t.Fatal("selected identity or cgroup was replaced")
					}
					return nil
				},
				containerPath: func(string) (string, error) { return path, nil },
				collect: func(ctx context.Context, pid int, options collector.Options) (*collector.Result, error) {
					if pid != 42 || *options.ExpectedIdentity != identity {
						t.Fatalf("collector options = %+v, pid = %d", options, pid)
					}
					if options.TopK != 7 {
						t.Fatalf("collector maximum memory object entries = %d, want 7", options.TopK)
					}
					for _, timeout := range []time.Duration{
						options.GoTimeout, options.JavaTimeout, options.PythonTimeout,
					} {
						if timeout != 3*time.Second {
							t.Fatalf("collector capture timeout = %s, want 3s", timeout)
						}
					}
					if err := options.CheckTarget(ctx, identity); err != nil {
						return nil, err
					}
					result.CaptureTime = time.Now().UTC()
					if changed {
						path = "/replacement"
					}
					return result, options.Save(ctx, result)
				},
				save: func(req *tracing.WriteRequest) error {
					saved = true
					if req.TracerName != "memory_threshold_snapshot" {
						t.Fatalf("tracer name = %q", req.TracerName)
					}
					data := req.TracerData.(*memoryThresholdSnapshotData)
					if data.Snapshot != result.Snapshot || data.Language != result.Language ||
						data.ProcessMemory != result.ProcessMemory ||
						!req.ObservedTimestamp.Equal(result.CaptureTime) {
						t.Fatalf("saved result = %+v", data)
					}
					return tracing.Save(req)
				},
			}
			cfg := &Config{}
			cfg.MemoryThresholdSnapshot.MaxMemoryObjectEntries = 7
			cfg.MemoryThresholdSnapshot.RunTracingToolTimeout = 3
			before := time.Now().UTC()
			err = (&memoryThresholdSnapshot{captureOps: ops}).captureCandidate(
				t.Context(), cfg, &memcgCandidate{cgroupPath: "/original"},
			)
			if changed && (err == nil || saved) {
				t.Fatalf("changed container path persisted: error=%v saved=%v", err, saved)
			}
			if !changed && (err != nil || !saved) {
				t.Fatalf("unchanged container not persisted: error=%v saved=%v", err, saved)
			}
			if err := store.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(outputDir, memoryThresholdSnapshotTracer))
			if changed {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("changed container snapshot exists: error=%v data=%s", err, raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct {
				types.Document
				TracerData memoryThresholdSnapshotData `json:"tracer_data"`
			}
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if err := persisted.Validate(); err != nil {
				t.Fatalf("persisted document is invalid: %v", err)
			}
			if persisted.TracerRunType != types.TracerRunTypeAutotracing {
				t.Fatalf("tracer type = %q, want autotracing", persisted.TracerRunType)
			}
			if persisted.StartedTimestamp == nil || persisted.StartedTimestamp.Before(before) ||
				persisted.StartedTimestamp.After(result.CaptureTime) {
				t.Fatalf("started timestamp = %v, want between %s and %s",
					persisted.StartedTimestamp, before, result.CaptureTime)
			}
			if persisted.ObservedTimestamp == nil || !persisted.ObservedTimestamp.Equal(result.CaptureTime) {
				t.Fatalf("observed timestamp = %v, want %s", persisted.ObservedTimestamp, result.CaptureTime)
			}
			if persisted.TracerName != memoryThresholdSnapshotTracer || persisted.TracerData.VictimPID != 42 ||
				persisted.TracerData.Snapshot == nil || persisted.TracerData.Snapshot.Status != memsnapshot.StatusComplete {
				t.Fatalf("persisted snapshot = %+v", persisted)
			}
		})
	}
}
