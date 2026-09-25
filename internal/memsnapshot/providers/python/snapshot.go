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

package python

import (
	"context"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// Leave time to reduce copied data and return a useful result after the last
// process_vm_readv batch.
const resultReserve = 20 * time.Millisecond

// reader reads CPython's own GC and object metadata from the named
// victim. It does not execute code in the victim and does not require a Python
// package, agent, hook, debug build, or periodic discovery.
type reader struct {
	procRoot string
}

type scanner struct {
	memory         memoryReader
	image          image
	deadline       time.Time
	types          map[uint64]typeInfo
	invalidTypes   map[uint64]struct{}
	listTypes      map[uint64]bool
	aggregates     map[uint64]*memsnapshot.ObjectAggregate
	typeNameBytes  int
	objectScratch  [16]byte
	scannedObjects int
	skippedObjects int
	partial        string
}

// newReader builds an on-demand CPython process-memory reader.
func newReader(procRoot string) *reader {
	if procRoot == "" {
		procRoot = "/proc"
	}
	return &reader{procRoot: procRoot}
}

// snapshot reads and aggregates objects currently tracked by CPython's cyclic
// garbage collector.
//
//nolint:gocritic // Readers receive an isolated request value from the provider.
func (r *reader) snapshot(ctx context.Context,
	request memsnapshot.Request,
) (*memsnapshot.Snapshot, error) {
	readTID := request.Identity.TGID
	if err := memsnapshot.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	deadline, hasDeadline := memsnapshot.DeadlineWithReserve(ctx,
		resultReserve)
	readCtx := ctx
	if hasDeadline {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	reader := newMemory(readTID, readCtx)
	target, err := discoverRuntime(readCtx, r.procRoot, readTID, reader)
	if err != nil {
		return nil, err
	}
	if err := memsnapshot.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	census := newScanner(reader, &target, deadline)
	snapshot, err := census.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := memsnapshot.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func newScanner(memory memoryReader, image *image, deadline time.Time) *scanner {
	imageCopy := *image
	return &scanner{
		memory: memory, image: imageCopy, deadline: deadline,
		types:        make(map[uint64]typeInfo),
		invalidTypes: make(map[uint64]struct{}),
		listTypes:    make(map[uint64]bool),
		aggregates:   make(map[uint64]*memsnapshot.ObjectAggregate),
	}
}

func (c *scanner) snapshot(ctx context.Context) (*memsnapshot.Snapshot, error) {
	interpreters, err := c.findInterpreters()
	if err != nil {
		return nil, err
	}

	c.walkGC(ctx, interpreters)

	return c.buildSnapshot(), nil
}

func (c *scanner) buildSnapshot() *memsnapshot.Snapshot {
	status := memsnapshot.StatusComplete
	reason := c.partial
	if c.skippedObjects != 0 {
		classificationReason := fmt.Sprintf(
			"%d GC-tracked objects could not be classified", c.skippedObjects,
		)
		if reason == "" {
			reason = classificationReason
		} else {
			reason += "; " + classificationReason
		}
	}
	if reason != "" {
		status = memsnapshot.StatusPartial
	}
	snapshot := &memsnapshot.Snapshot{
		RuntimeVersion: c.image.version.String(), Status: status,
		Reason: reason,
	}
	snapshot.Entries = c.entries()
	return snapshot
}

func (c *scanner) walkGC(ctx context.Context,
	interpreters []gcHeads,
) {
	for _, interpreter := range interpreters {
		// Frozen and old-generation objects are the strongest OOM signal: inspect
		// the permanent generation first, then work back toward short-lived gen0.
		for generation := len(interpreter.heads) - 1; generation >= 0; generation-- {
			head := interpreter.heads[generation]
			if !c.walkGeneration(ctx, head) {
				return
			}
		}
	}
}
