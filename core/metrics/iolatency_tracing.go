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

package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("iolatency", newIolatency)
}

func newIolatency() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &iolatencyTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_tracing.c -o $BPF_DIR/iolatency_tracing.o

const (
	blkDiskBucketMap        = "blkdisk_lat_map"
	blkContainerBucketMap   = "blkcg_lat_map"
	ioLatencyProbeResultMap = "io_latency_probe_result"
	ioLatencyBucketCount    = 17
	ioLatencyStageCount     = 3
	ioSizeBucketCount       = 6
)

type ioLatencyHostKey struct {
	Major     uint32
	Minor     uint32
	Operation uint32
}

type ioLatencyContainerKey struct {
	Blkcg     uint64
	Serial    uint64
	Major     uint32
	Minor     uint32
	Operation uint32
	Kind      uint32
}

type ioLatencyCounters struct {
	Buckets    [ioLatencyStageCount][ioLatencyBucketCount]uint64
	QueuedSize [ioSizeBucketCount]uint64
	IssuedSize [ioSizeBucketCount]uint64
}

type ioLatencyDiskCounters struct {
	Major    uint32
	Minor    uint32
	Counters [2]ioLatencyCounters
	SCSI     uint64
}

type ioLatencySession struct {
	object   bpf.BPF
	previous *ioLatencySnapshot
	cancel   context.CancelCauseFunc
	devices  map[[2]uint32]string
}

type iolatencyTracing struct {
	mu      sync.Mutex
	session *ioLatencySession
}

func (c *iolatencyTracing) Start(ctx context.Context) (retErr error) {
	constants, err := loadIOLatencyKernelConstants()
	if err != nil {
		log.Warnf("iolatency: startup kernel check: %v", err)
		return err
	}
	var arguments map[string]any
	if err := retryIOControlAttach(ctx, ioControlAttachRetryInterval, func() error {
		arguments, err = loadIOLatencyTracepointArguments()
		return err
	}); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("iolatency: tracepoint ABI unavailable: %v", err)
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	for name, argument := range arguments {
		constants[name] = argument
	}
	constants["io_latency_containers_enabled"] = pod.ContainerSyncEnabled()
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), constants)
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancelCause(ctx)
	defer func() {
		if cause := context.Cause(childCtx); errors.Is(cause, types.ErrTracingStopped) {
			retErr = cause
		}
		cancel(nil)
	}()
	session := &ioLatencySession{
		object:   b,
		cancel:   cancel,
		previous: &ioLatencySnapshot{},
	}
	if err := retryIOControlAttach(childCtx, ioControlAttachRetryInterval, func() error {
		return attachIOLatencyHooks(b)
	}); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			log.Warnf("iolatency: required hooks unavailable: %v", err)
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	if err := session.discoverDisks(procfs.DefaultPathByType("sys")); err != nil {
		log.Warnf("iolatency: startup queue check: %v", err)
		return err
	}
	if err := b.Detach("probe_queue_stats"); err != nil {
		return fmt.Errorf("detach completed iolatency disk probe: %w", err)
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	defer c.withdrawSession()

	<-childCtx.Done()
	return nil
}

func (c *iolatencyTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachIOLatencyHooks(object bpf.BPF) error {
	options := append(ioControlDeviceHooks(),
		bpf.AttachOption{
			ProgramName: "probe_queue_stats",
			Symbol:      "queue_attr_show",
		},
		bpf.AttachOption{
			ProgramName: "io_latency_css_released", Symbol: "blkcg_css_free",
		},
	)
	// Every selected hook is required; Start closes a partial attachment before
	// publishing a session.
	for _, definition := range ioLatencyTracepoints {
		options = append(options, bpf.AttachOption{
			ProgramName: definition.program,
			Symbol:      definition.symbol,
		})
	}
	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach iolatency hooks: %w", err)
	}
	return nil
}
