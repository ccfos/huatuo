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

// Package dropwatch owns BPF resources for software and hardware drop tracing.
package dropwatch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

// Tracer owns one active dropwatch instance. Open must initialize it; it must
// not be copied. ReadInto permits one reader. Close may run concurrently with
// reads, but the owner must serialize calls to Close.
type Tracer struct {
	bpf     bpf.BPF
	reader  bpf.PerfEventReader
	limiter *bpf.RateLimiter
	// The context spans this resource's lifetime, including blocking reads.
	ctx               context.Context
	cancel            context.CancelCauseFunc
	workers           errgroup.Group
	hardwareEnabled   bool
	isClosed          atomic.Bool
	perfStatusMap     uint32
	rateLimitStateMap uint32
	lostSamples       atomic.Uint64
}

// Open loads, configures and attaches a dropwatch instance. ctx controls the
// entire tracing session, not just initialization. Cancellation stops event
// reading but does not release BPF resources; the caller must still Close the
// tracer. The caller owns bpf.Init/Shutdown.
func Open(ctx context.Context, cfg *Config) (*Tracer, error) {
	if cfg == nil {
		return nil, errors.New("open dropwatch: nil config")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	filter, err := resolveNetdevOptions(cfg.IncludeDevices, cfg.ExcludeDevices)
	if err != nil {
		return nil, err
	}

	hardwareEnabled, err := resolveHardwareEnabled(cfg.HardwareMode)
	if err != nil {
		return nil, err
	}

	limiter := bpf.NewRateLimiter("dropwatch", cfg.MaxEventsPerSecond)

	bpfObject, err := loadBPF(cfg, filter.mode, limiter, hardwareEnabled)
	if err != nil {
		return nil, fmt.Errorf("load dropwatch: %w", err)
	}
	return newTracer(ctx, bpfObject, filter, limiter, hardwareEnabled)
}

// newTracer takes ownership of object, attaches probes and starts the alert
// worker. Setup failures release all acquired resources.
func newTracer(
	ctx context.Context,
	object bpf.BPF,
	filter netdevOptions,
	limiter *bpf.RateLimiter,
	hardwareEnabled bool,
) (_ *Tracer, returnErr error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	tracer := &Tracer{
		bpf:             object,
		limiter:         limiter,
		ctx:             runCtx,
		cancel:          cancel,
		hardwareEnabled: hardwareEnabled,
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, tracer.Close())
		}
	}()

	if err := tracer.attachBPF(filter); err != nil {
		return nil, err
	}
	if limiter.Enabled() {
		tracer.workers.Go(func() error {
			err := limiter.ReadEvents(runCtx)
			if err != nil {
				cancel(err)
			}
			return err
		})
	}

	return tracer, nil
}

// HardwareEnabled reports whether this instance attached the devlink program.
func (t *Tracer) HardwareEnabled() bool { return t.hardwareEnabled }

// ReadInto reads one event into caller-owned memory. Only a nil error makes dst
// usable. Sample loss is counted before returning bpf.PerfEventSamplesLostError
// and may be retried. Cancellation interrupts idle reads within the reader's
// polling interval; Close interrupts them. Before reading, closure returns
// bpf.ErrClosed and cancellation returns its cause. Once reading starts, the
// underlying result is returned unchanged, including types.ErrExitByCancelCtx
// on cancellation or closure. Alert worker errors remain available from Close.
func (t *Tracer) ReadInto(dst *abi.DropwatchPacketEvent) error {
	if dst == nil {
		return errors.New("read dropwatch event: nil destination")
	}
	if t.isClosed.Load() {
		return bpf.ErrClosed
	}
	if err := context.Cause(t.ctx); err != nil {
		return err
	}
	err := t.reader.ReadInto(dst)
	if err == nil {
		return nil
	}
	var lostErr *bpf.PerfEventSamplesLostError
	if errors.As(err, &lostErr) {
		t.lostSamples.Add(lostErr.Count)
	}
	return err
}

// Close interrupts reads and releases all owned resources. The owner must
// serialize calls; subsequent calls return nil without retrying cleanup or
// replaying errors. Cancellation alone leaves maps available for final status
// queries until Close.
func (t *Tracer) Close() error {
	if t.isClosed.Swap(true) {
		return nil
	}
	t.cancel(bpf.ErrClosed)
	detachErr := t.bpf.Detach()
	var readerErr error
	if t.reader != nil {
		readerErr = t.reader.Close()
	}
	// Wake an idle alert read instead of waiting for its polling deadline.
	limiterErr := t.limiter.CloseEventPipe()
	workerErr := t.workers.Wait()
	objectErr := t.bpf.Close()
	return errors.Join(detachErr, readerErr, workerErr, limiterErr, objectErr)
}
