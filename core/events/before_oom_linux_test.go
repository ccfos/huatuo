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
	"context"
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
	"github.com/ccfos/huatuo/internal/memsnap"
	"github.com/ccfos/huatuo/internal/memsnap/collector"
	"github.com/ccfos/huatuo/internal/tracing"
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

func TestBeforeOOMStopJoinsWatcherBeforeRestart(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.limit_in_bytes", "memory.usage_in_bytes", "cgroup.event_control"} {
		writeMemoryEventsForTest(t, filepath.Join(directory, name), "0")
	}
	cfg := &BeforeOOMConfig{ThresholdPercent: 90}
	snapshot := &beforeOOMMemsnap{}
	for iteration := 0; iteration < 2; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			backend := &blockingMemoryCgroup{entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(backend.release) })
			watcher, err := newPressureWatcher(backend, cfg)
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

func writeMemoryEventsForTest(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeforeOOMRevalidatesContainerBeforePersistence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			path, saved := "/original", false
			identity := memsnap.ProcessIdentity{TGID: 42, StartTimeTicks: 100}
			result := &collector.Result{
				Identity: identity, Language: memsnap.LanguageGo,
				CaptureTime: time.Now().UTC(),
				Snapshot:    &memsnap.Snapshot{Status: memsnap.StatusComplete},
			}
			ops := &beforeOOMOps{
				selectVictim: func(context.Context, string, uint64) (victimCandidate, error) {
					return victimCandidate{pid: 42, identity: identity}, nil
				},
				validate: func(_ context.Context, path string, actual memsnap.ProcessIdentity) error {
					if path != "/original" || actual != identity {
						t.Fatal("selected identity or cgroup was replaced")
					}
					return nil
				},
				containerPath: func(string) (string, error) { return path, nil },
				collect: func(ctx context.Context, pid int, options collector.Options) (*collector.Result, error) {
					if pid != 42 || *options.ExpectedIdentity != identity || options.TopK != 10 ||
						options.GoTimeout != 100*time.Millisecond ||
						options.JavaTimeout != 2*time.Second || options.PythonTimeout != 2*time.Second {
						t.Fatalf("collector options = %+v, pid = %d", options, pid)
					}
					if err := options.CheckTarget(ctx, identity); err != nil {
						return nil, err
					}
					if changed {
						path = "/replacement"
					}
					return result, options.Save(ctx, result)
				},
				save: func(req *tracing.WriteRequest) error {
					saved = true
					data := req.TracerData.(*beforeOOMData)
					if data.Snapshot != result.Snapshot || data.Language != result.Language ||
						!req.ObservedTimestamp.Equal(result.CaptureTime) {
						t.Fatalf("saved result = %+v", data)
					}
					return nil
				},
			}
			err := (&beforeOOMMemsnap{captureOps: ops}).captureCandidate(t.Context(),
				&BeforeOOMConfig{
					TopK: 10, GoTimeoutMS: 100,
					JavaTimeoutMS: 2000, PythonTimeoutMS: 2000,
				},
				&memcgCandidate{cgroupPath: "/original"})
			if changed && (err == nil || saved) {
				t.Fatalf("changed container path persisted: error=%v saved=%v", err, saved)
			}
			if !changed && (err != nil || !saved) {
				t.Fatalf("unchanged container not persisted: error=%v saved=%v", err, saved)
			}
		})
	}
}
