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

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// Provider reads Go runtime heap profiling metadata from a running process.
// Each call owns its buffers and aggregates; Provider retains no sampling state.
type Provider struct{}

// New builds the production Go snapshot provider.
func New() *Provider {
	return &Provider{}
}

// Snapshot expects the caller to validate the request identity and MaxMemoryObjectEntries.
// Unavailable or partial data is a snapshot; fatal read failures and cancellation
// return an error without a snapshot. The caller's context owns the time budget;
// cancellation and deadline expiry take precedence over collected data.
func (p *Provider) Snapshot(ctx context.Context,
	request memsnapshot.Request,
) (snapshot *memsnapshot.Snapshot, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			snapshot, err = nil, ctxErr
		}
	}()

	reader, err := newProcessReader(ctx, request.Process)
	if err != nil {
		if errors.Is(err, errUnsupportedRuntime) || errors.Is(err, errMBucketsSymbolNotFound) {
			return memsnapshot.Unavailable(err.Error()), nil
		}
		return nil, fmt.Errorf("read Go runtime metadata: %w", err)
	}
	defer reader.Close()

	scan, err := reader.scanHeapProfile(ctx, request.MaxMemoryObjectEntries)
	if err != nil {
		return nil, fmt.Errorf("read Go runtime mbuckets: %w", err)
	}

	result := &memsnapshot.Snapshot{
		RuntimeVersion:  reader.runtime.version,
		Status:          scan.status,
		StatusReason:    scan.reason,
		OutputTruncated: scan.hasOmittedAllocations,
	}

	if scan.status == memsnapshot.SnapshotStatusUnavailable {
		return result, nil
	}
	if len(scan.allocations) == 0 {
		return result, nil
	}

	symbols, symbolErr := reader.buildSymbolizer(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if symbolErr != nil {
		return nil, symbolErr
	}

	entries, err := buildEntries(ctx, scan.allocations, reader.runtime.layout.byteOrder, symbols)
	if err != nil {
		return nil, err
	}
	result.Entries = entries
	return result, nil
}
