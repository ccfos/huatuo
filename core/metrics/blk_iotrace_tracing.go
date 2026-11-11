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
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("blk_iotrace", newBlkIOTrace)
}

func newBlkIOTrace() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &blkIOTraceTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/blk_iotrace_tracing.c -o $BPF_DIR/blk_iotrace_tracing.o

const (
	blkDiskBucketMap         = "blkdisk_lat_map"
	blkContainerBucketMap    = "blkcg_lat_map"
	blkIOTraceProbeResultMap = "blk_iotrace_probe_result"
	blkIOTraceBucketCount    = int(abi.BlkIotraceBuckets)
	blkIOTraceStageCount     = int(abi.BlkIotraceStageCount)
	ioSizeBucketCount        = int(abi.IOSizeBuckets)
)

type blkIOTraceHostKey struct {
	Major     uint32
	Minor     uint32
	Operation uint32
}

type blkIOTraceContainerKey = abi.BlkIotraceContainerKey

type blkIOTraceCounters = abi.BlkIotraceCounters

type blkIOTraceDiskCounters = abi.BlkIotraceHostCounters

type blkIOTraceSession struct {
	object   bpf.BPF
	previous *blkIOTraceSnapshot
	cancel   context.CancelCauseFunc
	devices  map[[2]uint32]string
}

type blkIOTraceTracing struct {
	mu      sync.Mutex
	session *blkIOTraceSession
}

func (c *blkIOTraceTracing) Start(ctx context.Context) (retErr error) {
	constants, err := loadBlkIOTraceKernelConstants()
	if err != nil {
		log.WithError(err).Warn("blk_iotrace: startup kernel check")
		return err
	}
	var arguments map[string]any
	if err := retryIOControlAttach(ctx, ioControlAttachRetryInterval, func() error {
		arguments, err = loadBlkIOTraceTracepointArguments()
		return err
	}); err != nil {
		if err == ctx.Err() { //nolint:errorlint // Wrapped cleanup failures must remain visible.
			return nil
		}
		if errors.Is(err, types.ErrNotSupported) {
			log.WithError(err).Warn("blk_iotrace: tracepoint ABI unavailable")
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	for name, argument := range arguments {
		constants[name] = argument
	}
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
	session := &blkIOTraceSession{
		object:   b,
		cancel:   cancel,
		previous: &blkIOTraceSnapshot{},
	}
	if err := retryIOControlAttach(childCtx, ioControlAttachRetryInterval, func() error {
		return attachBlkIOTraceHooks(b)
	}); err != nil {
		if err == childCtx.Err() { //nolint:errorlint // Wrapped cleanup failures must remain visible.
			return nil
		}
		if errors.Is(err, types.ErrNotSupported) {
			log.WithError(err).Warn("blk_iotrace: required hooks unavailable")
		}
		return fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
	}
	if err := session.discoverDisks(procfs.DefaultPathByType("sys")); err != nil {
		log.WithError(err).Warn("blk_iotrace: startup queue check")
		return err
	}
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	defer c.withdrawSession()

	<-childCtx.Done()
	return nil
}

func (c *blkIOTraceTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachBlkIOTraceHooks(object bpf.BPF) error {
	options, err := ioControlDeviceHooks()
	if err != nil {
		return err
	}
	options = append(
		options,
		bpf.AttachOption{
			ProgramName: "probe_queue_stats",
			Symbol:      "queue_attr_show",
		},
		bpf.AttachOption{
			ProgramName: "blk_iotrace_css_released",
			Symbol:      "blkcg_css_free",
		},
	)
	// Every selected hook is required; Start closes a partial attachment before
	// publishing a session.
	for _, definition := range blkIOTraceTracepoints {
		options = append(options, bpf.AttachOption{
			ProgramName: definition.program,
			Symbol:      definition.symbol,
		})
	}
	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach blk_iotrace hooks: %w", err)
	}
	return nil
}
