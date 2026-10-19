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

package main

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/profile"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
)

func TestIRQTracingBPFConstants(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		constants := irqTracingBPFConstants(allCPUsTarget, 0)
		if len(constants) != 1 || constants["target_cpu"] != allCPUsTarget {
			t.Fatalf("constants = %#v, want only target_cpu", constants)
		}
	})

	t.Run("splits per-CPU limit", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, 1000)
		if got := constants[sourceRateLimitConstant]; got != uint32(500) {
			t.Fatalf("source rate = %v, want 500", got)
		}

		if got := constants[victimRateLimitConstant]; got != uint32(500) {
			t.Fatalf("victim rate = %v, want 500", got)
		}
	})

	t.Run("keeps odd total", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, 1001)
		if got := constants[sourceRateLimitConstant]; got != uint32(501) {
			t.Fatalf("source rate = %v, want 501", got)
		}

		if got := constants[victimRateLimitConstant]; got != uint32(500) {
			t.Fatalf("victim rate = %v, want 500", got)
		}
	})

	t.Run("keeps maximum total", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, 2*uint64(math.MaxUint32))
		if got := constants[sourceRateLimitConstant]; got != uint32(math.MaxUint32) {
			t.Fatalf("source rate = %v, want %d", got, uint32(math.MaxUint32))
		}

		if got := constants[victimRateLimitConstant]; got != uint32(math.MaxUint32) {
			t.Fatalf("victim rate = %v, want %d", got, uint32(math.MaxUint32))
		}
	})
}

func TestSoftIRQCancellationDoesNotSubmitPartialBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &collector{}
	submitted := false
	err := c.ReadDataLoop(ctx, func(*pipeline.SampleBatch) error { submitted = true; return nil })
	if !errors.Is(err, context.Canceled) || submitted {
		t.Fatalf("error=%v submitted=%v", err, submitted)
	}
}

func TestNewIRQProfilerUsesOneShotAggregation(t *testing.T) {
	task := newIRQProfiler(&collector{})
	if task.AggregationMode != pipeline.AggregationModeOneShot {
		t.Fatalf("aggregation mode=%v", task.AggregationMode)
	}
}

func TestSoftIRQNormalStopPreservesDetachFailure(t *testing.T) {
	failure := errors.New("detach probes failed")
	for _, cause := range []error{profile.ErrCollectionDurationElapsed, profile.ErrStopRequested} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)
			c := &collector{bpfObj: &orderMockBPF{detachErr: failure}}
			err := c.ReadDataLoop(ctx, func(*pipeline.SampleBatch) error { t.Fatal("delivered after detach failure"); return nil })
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
