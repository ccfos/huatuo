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

// Package retransmit runs TCP retransmit tracing and optional drop correlation.
package retransmit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

// Tracer must be initialized by Open and must not be copied. ReadInto permits
// one reader. Close may interrupt reads; the owner must serialize Close calls.
type Tracer struct {
	bpf     bpf.BPF
	reader  bpf.PerfEventReader
	limiter *bpf.RateLimiter
	// The context controls the entire resource lifetime, including idle reads.
	ctx      context.Context
	cancel   context.CancelCauseFunc
	workers  errgroup.Group
	isClosed atomic.Bool
}

// Open loads and attaches a tracer. ctx controls the entire tracing session;
// cancellation stops reading but does not release resources. The caller must
// Close the tracer and owns process-wide bpf.Init/Shutdown.
func Open(ctx context.Context, cfg *Config) (*Tracer, error) {
	if cfg == nil {
		return nil, errors.New("open retransmit: nil config")
	}
	if cfg.BPFPath == "" {
		return nil, errors.New("retransmit: BPF path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limiter := bpf.NewRateLimiter("tcp_retransmit", cfg.MaxEventsPerSecond)
	object, err := loadBPF(cfg.BPFPath, cfg.FilterExpression, limiter)
	if err != nil {
		return nil, fmt.Errorf("load retransmit: %w", err)
	}
	return newTracer(ctx, object, limiter, cfg.TLPEnabled)
}

// newTracer takes ownership of object, including rollback on setup failure.
func newTracer(ctx context.Context, object bpf.BPF, limiter *bpf.RateLimiter, tlpEnabled bool) (_ *Tracer, returnErr error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	t := &Tracer{bpf: object, limiter: limiter, ctx: runCtx, cancel: cancel}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, t.Close())
		}
	}()
	if err := t.attachBPF(tlpEnabled); err != nil {
		return nil, err
	}
	if limiter.Enabled() {
		t.workers.Go(func() error {
			err := limiter.ReadEvents(runCtx)
			if err != nil {
				cancel(err)
			}
			return err
		})
	}
	return t, nil
}

// ReadInto reads one event into caller-owned memory. Only a nil error makes dst
// usable. Before reading, closure returns bpf.ErrClosed and cancellation returns
// its cause. Once reading starts, the underlying result is preserved, including
// sample-loss and cancellation errors. Alert worker failures remain in Close.
func (t *Tracer) ReadInto(dst *abi.TCPRetransmitEvent) error {
	if dst == nil {
		return errors.New("read retransmit event: nil destination")
	}
	if t.isClosed.Load() {
		return bpf.ErrClosed
	}
	if err := context.Cause(t.ctx); err != nil {
		return err
	}
	return t.reader.ReadInto(dst)
}

// Close interrupts reads and releases resources, joining cleanup and worker
// errors. Subsequent calls return nil without retrying cleanup or replaying
// errors. Calls must be serialized by the owner.
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
	// Wake the alert reader before waiting for its worker.
	limiterErr := t.limiter.CloseEventPipe()
	workerErr := t.workers.Wait()
	objectErr := t.bpf.Close()
	return errors.Join(detachErr, readerErr, limiterErr, workerErr, objectErr)
}
