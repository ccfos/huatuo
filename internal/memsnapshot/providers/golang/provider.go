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
	"strings"

	"github.com/ccfos/huatuo/internal/memsnapshot"
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
	RuntimeVersion string
	SampleRate     int64
	RateKnown      bool
	Allocations    []sample
	PartialReason  string
	HasOmittedData bool
}

// Provider captures a Go runtime heap snapshot through the external reader.
type Provider struct {
	reader *reader
}

// New builds the production Go snapshot provider.
func New() *Provider {
	return &Provider{reader: newReader("")}
}

// Snapshot reads the victim Go heap and reduces it to allocation-site entries.
// Unavailable or partial data is a snapshot; fatal read failures and cancellation
// return an error without a snapshot.
func (p *Provider) Snapshot(ctx context.Context,
	request memsnapshot.Request,
) (*memsnapshot.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	snapshot, err := p.reader.snapshot(ctx, request.Process, request.TopK)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	return snapshotResult(snapshot, err)
}

func snapshotResult(snapshot *snapshot, err error) (*memsnapshot.Snapshot, error) {
	if errors.Is(err, errUnsupportedRuntime) ||
		errors.Is(err, errMBucketsSymbolNotFound) {
		return memsnapshot.Unavailable(err.Error()), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Go runtime mbuckets: %w", err)
	}

	return resultFromSnapshot(snapshot)
}

func resultFromSnapshot(snapshot *snapshot) (*memsnapshot.Snapshot, error) {
	if snapshot == nil {
		return nil, errors.New("Go external heap reader returned a nil snapshot")
	}
	if snapshot.RateKnown && snapshot.SampleRate <= 0 {
		return memsnapshot.Unavailable("Go heap profiling is disabled by MemProfileRate=0"), nil
	}
	status := memsnapshot.StatusComplete
	reason := ""
	if !snapshot.RateKnown {
		status = memsnapshot.StatusPartial
		reason = "runtime.MemProfileRate is unavailable; values are unscaled samples"
	}
	if snapshot.PartialReason != "" {
		status = memsnapshot.StatusPartial
		if reason != "" {
			reason += "; "
		}
		reason += snapshot.PartialReason
	}
	result := &memsnapshot.Snapshot{
		RuntimeVersion: snapshot.RuntimeVersion, Status: status, Reason: reason,
		HasOmittedData: snapshot.HasOmittedData,
	}
	result.Entries = make([]memsnapshot.Entry, 0, len(snapshot.Allocations))
	for _, allocation := range snapshot.Allocations {
		if len(allocation.Stack) == 0 || len(allocation.Stack) > maxStackDepth {
			continue
		}
		average := float64(0)
		if allocation.Objects != 0 {
			average = float64(allocation.Bytes) / float64(allocation.Objects)
		}
		result.Entries = append(result.Entries, memsnapshot.Entry{
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
