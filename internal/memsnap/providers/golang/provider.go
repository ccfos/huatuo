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

package golang

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ccfos/huatuo/internal/memsnap"
)

var errUnsupportedRuntime = errors.New("Go runtime is unsupported")

// maxStackDepth bounds one externally copied runtime.memProfile stack.
const maxStackDepth = 64

// sample is one selected allocation-stack aggregate. Stack order is allocation
// site first, matching runtime.MemProfileRecord.Stack.
type sample struct {
	Stack   []string
	Bytes   uint64
	Objects uint64
}

// snapshot contains the copied Go heap-profile metadata.
type snapshot struct {
	RuntimeVersion  string
	SampleRate      int64
	RateKnown       bool
	Allocations     []sample
	PartialReason   string
	OutputTruncated bool
}

// Provider captures a Go runtime heap snapshot through the external reader.
type Provider struct {
	reader *reader
}

// NewProvider builds the production Go snapshot provider.
func NewProvider() *Provider {
	return &Provider{reader: newReader("")}
}

// Capture reads the victim Go heap and reduces it to allocation-site entries.
func (p *Provider) Capture(ctx context.Context,
	request memsnap.Request,
) (*memsnap.Snapshot, error) {
	snapshot, err := p.reader.capture(ctx, request.Identity, request.TopK)
	return captureResult(snapshot, err), nil
}

func captureResult(snapshot *snapshot, err error) *memsnap.Snapshot {
	if errors.Is(err, errUnsupportedRuntime) ||
		errors.Is(err, errMBucketsSymbolNotFound) {
		return memsnap.Unavailable(err.Error())
	}
	if err != nil {
		return memsnap.Failed(fmt.Sprintf("read Go runtime mbuckets: %v", err))
	}
	result, err := resultFromSnapshot(snapshot)
	if err != nil {
		return memsnap.Failed(err.Error())
	}
	return result
}

func resultFromSnapshot(snapshot *snapshot) (*memsnap.Snapshot, error) {
	if snapshot == nil {
		return nil, errors.New("Go external heap reader returned a nil snapshot")
	}
	if snapshot.RateKnown && snapshot.SampleRate <= 0 {
		return memsnap.Unavailable("Go heap profiling is disabled by MemProfileRate=0"), nil
	}
	status := memsnap.StatusComplete
	reason := ""
	if !snapshot.RateKnown {
		status = memsnap.StatusPartial
		reason = "runtime.MemProfileRate is unavailable; values are unscaled samples"
	}
	if snapshot.PartialReason != "" {
		status = memsnap.StatusPartial
		if reason != "" {
			reason += "; "
		}
		reason += snapshot.PartialReason
	}
	result := &memsnap.Snapshot{
		RuntimeVersion: snapshot.RuntimeVersion, Status: status, Reason: reason,
		OutputTruncated: snapshot.OutputTruncated,
	}
	result.Entries = make([]memsnap.Entry, 0, len(snapshot.Allocations))
	for _, allocation := range snapshot.Allocations {
		if len(allocation.Stack) == 0 || len(allocation.Stack) > maxStackDepth {
			continue
		}
		average := float64(0)
		if allocation.Objects != 0 {
			average = float64(allocation.Bytes) / float64(allocation.Objects)
		}
		result.Entries = append(result.Entries, memsnap.Entry{
			Kind: "allocation_site", Name: allocationSiteName(allocation.Stack), Bytes: allocation.Bytes,
			Objects: allocation.Objects, AverageBytes: average, Stack: allocation.Stack,
		})
	}
	return result, nil
}

// allocationSiteName follows runtime/pprof's presentation rule for allocation
// traces: hide leading runtime implementation frames when a caller frame is
// available, but preserve the complete raw stack in the emitted Entry.
func allocationSiteName(stack []string) string {
	for _, frame := range stack {
		if !strings.HasPrefix(frame, "runtime.") &&
			!strings.HasPrefix(frame, "internal/runtime/") {
			return frame
		}
	}
	return stack[0]
}

func clampUint64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func saturatedInt64Add(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}
