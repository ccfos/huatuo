// Copyright 2025, 2026 The HuaTuo Authors
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

package pod

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cgroup_css_events.c -o $BPF_DIR/cgroup_css_events.o

type containerCssPerfEvent = abi.CgroupCSSEvent

var (
	kubeletContainerIDRegexp = regexp.MustCompile(`(?:cri-containerd-)?([0-9a-f]{64})(?:\.scope)?`)
	cgroupCssBpfInternal     *bpf.BPF
	cgroupCssBpfCancelFunc   context.CancelFunc
	cgroupLifecycleDone      <-chan struct{}

	// The default backend registers its local CSS cache at package initialization.
	// Lifecycle notifications do not require CSS address discovery.
	cgroupCSSCacheInit  func() error
	cgroupCSSCacheSync  func() error
	cgroupCSSCacheEvent func(*containerCssPerfEvent, string)
)

func cgroupCssEventSyncHandler(ctx context.Context, reader bpf.PerfEventReader, lifecycle bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				var data containerCssPerfEvent
				if err := reader.ReadInto(&data); err != nil {
					if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
						log.WithError(err).Warn("lost BPF perf event samples")
						if lifecycle {
							requestContainerRefresh("", false)
						}
						continue
					}
					if ctx.Err() == nil && !errors.Is(err, types.ErrExitByCancelCtx) {
						log.Errorf("cgroup css sync read events: %v", err)
					}
					return
				}

				// Parse once: lifecycle notifications and the CSS cache share the ID.
				containerID := extractContainerID(bytesutil.ToStr(data.KnodeName[:]))

				if cgroupCSSCacheEvent != nil {
					cgroupCSSCacheEvent(&data, containerID)
				}
				if lifecycle && containerID != "" &&
					(data.Operation == abi.CgroupCSSOperationUpdate || data.Operation == abi.CgroupCSSOperationRemove) {
					requestContainerRefresh(containerID, data.Operation == abi.CgroupCSSOperationRemove)
				}
			}
		}
	}()
	return done
}

func cgroupCssInitEventSync() error {
	cssBpf, err := bpf.LoadBPF("cgroup_css_events.o", nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	cgroupCssBpfInternal = &cssBpf

	childCtx, cancel := context.WithCancel(context.Background())
	cgroupCssBpfCancelFunc = cancel

	reader, err := cssBpf.AttachAndEventPipe(childCtx, "cgroup_perf_events", bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		cancel()
		cssBpf.Close()
		cgroupCssBpfInternal = nil
		cgroupCssBpfCancelFunc = nil
		return err
	}
	done := make(chan struct{})
	cgroupLifecycleDone = done
	go func() {
		defer close(done)
		superviseCgroupCssEventLoop(childCtx, reader, func() (bpf.PerfEventReader, error) {
			return cssBpf.EventPipeByName(childCtx, "cgroup_perf_events", bpf.DefaultPerfEventBufferBytes)
		})
	}()
	return nil
}

// superviseCgroupCssEventLoop maintains CSS event delivery with reader recovery until cancellation.
func superviseCgroupCssEventLoop(ctx context.Context, reader bpf.PerfEventReader,
	reopen func() (bpf.PerfEventReader, error),
) {
	for {
		current := reader
		stop := context.AfterFunc(ctx, func() { _ = current.Close() })
		<-cgroupCssEventSyncHandler(ctx, reader, true)
		stop()
		_ = reader.Close()
		if ctx.Err() != nil {
			return
		}
		requestContainerRefresh("", false)
		for delay := time.Second; ; {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			var err error
			reader, err = reopen()
			if err == nil {
				log.Info("cgroup CSS event pipe recovered")
				// Recover changes lost while the event pipe was unavailable.
				requestContainerRefresh("", false)
				break
			}
			log.WithError(err).Warn("reopen cgroup CSS event pipe")
			delay = min(delay*2, 30*time.Second)
		}
	}
}

var (
	cgroupPodLifecycleMu    sync.Mutex
	cgroupPodLifecycleOwned bool
)

func initCgroupLifecycle() error {
	cgroupPodLifecycleMu.Lock()
	defer cgroupPodLifecycleMu.Unlock()
	if cgroupPodLifecycleOwned {
		return nil
	}
	if cgroupCSSCacheInit != nil {
		if err := cgroupCSSCacheInit(); err != nil {
			return err
		}
	}
	if err := cgroupCssInitEventSync(); err != nil {
		return err
	}
	if cgroupCSSCacheSync != nil {
		if err := cgroupCSSCacheSync(); err != nil {
			closeCgroupLifecycle()
			return err
		}
	}
	cgroupPodLifecycleOwned = true
	return nil
}

func extractContainerID(fileName string) string {
	got := kubeletContainerIDRegexp.FindStringSubmatch(fileName)
	if len(got) > 0 {
		return got[1]
	}
	return ""
}

func releaseCgroupLifecycle() {
	cgroupPodLifecycleMu.Lock()
	defer cgroupPodLifecycleMu.Unlock()
	if cgroupPodLifecycleOwned {
		cgroupPodLifecycleOwned = false
		closeCgroupLifecycle()
	}
}

func closeCgroupLifecycle() {
	if cgroupCssBpfCancelFunc != nil {
		cgroupCssBpfCancelFunc()
		cgroupCssBpfCancelFunc = nil
	}
	if cgroupLifecycleDone != nil {
		<-cgroupLifecycleDone
		cgroupLifecycleDone = nil
	}
	if cgroupCssBpfInternal != nil {
		(*cgroupCssBpfInternal).Close()
		cgroupCssBpfInternal = nil
	}
}
