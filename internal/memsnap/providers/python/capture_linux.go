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
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxRuntimeProbeBytes = 1024
	maxInterpreterBytes  = 4096
	// Leave time to reduce copied data and return a useful result after the last
	// process_vm_readv batch.
	resultReserve        = 20 * time.Millisecond
	maxInterpreterCount  = 1024
	maxInterpreterProbes = 4096
	debugOffsetsBytes    = 448
	debugVersion         = 8
	debugFreeThreaded    = 16
	debugRuntimeHead     = 40
	debugInterpreterSize = 48
	debugInterpreterNext = 64
	gcGenerationsOffset  = 24
)

// reader reads CPython's own GC and object metadata from the named
// victim. It does not execute code in the victim and does not require a Python
// package, agent, hook, debug build, or periodic discovery.
type reader struct {
	procRoot string
}

type gcHeads struct {
	heads [4]uint64
}

type scanner struct {
	memory         memoryReader
	image          image
	deadline       time.Time
	types          map[uint64]typeInfo
	invalidTypes   map[uint64]struct{}
	listTypes      map[uint64]bool
	aggregates     map[uint64]*memsnap.ObjectAggregate
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

// capture reads and aggregates objects currently tracked by CPython's cyclic
// garbage collector.
//
//nolint:gocritic // Readers receive an isolated request value from the provider.
func (r *reader) capture(ctx context.Context,
	request memsnap.Request,
) (*memsnap.Snapshot, error) {
	readTID := request.Identity.TGID
	if err := memsnap.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	deadline, hasDeadline := memsnap.DeadlineWithReserve(ctx,
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
	if err := memsnap.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	census := newScanner(reader, &target, deadline)
	response, err := census.capture(ctx)
	if err != nil {
		return nil, err
	}
	if err := memsnap.ValidateIdentity(r.procRoot, request.Identity); err != nil {
		return nil, err
	}
	return response, nil
}

func newScanner(memory memoryReader, image *image, deadline time.Time) *scanner {
	imageCopy := *image
	return &scanner{
		memory: memory, image: imageCopy, deadline: deadline,
		types:        make(map[uint64]typeInfo),
		invalidTypes: make(map[uint64]struct{}),
		listTypes:    make(map[uint64]bool),
		aggregates:   make(map[uint64]*memsnap.ObjectAggregate),
	}
}

func (c *scanner) capture(ctx context.Context) (*memsnap.Snapshot, error) {
	interpreters, err := c.findInterpreters()
	if err != nil {
		return nil, err
	}

	c.walkGC(ctx, interpreters)

	return c.response(), nil
}

func (c *scanner) response() *memsnap.Snapshot {
	status := memsnap.StatusComplete
	reason := c.partial
	if c.skippedObjects != 0 {
		classificationReason := fmt.Sprintf(
			"%d GC-tracked objects could not be classified", c.skippedObjects)
		if reason == "" {
			reason = classificationReason
		} else {
			reason += "; " + classificationReason
		}
	}
	if reason != "" {
		status = memsnap.StatusPartial
	}
	response := &memsnap.Snapshot{
		RuntimeVersion: c.image.version.String(), Status: status,
		Reason: reason,
	}
	response.Entries = c.entries()
	return response
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

func (c *scanner) findInterpreters() ([]gcHeads, error) {
	runtimeRaw, err := c.memory.read(c.image.runtimeAddress, maxRuntimeProbeBytes)
	if err != nil {
		return nil, fmt.Errorf("read _PyRuntime observability prefix: %w", err)
	}
	switch c.image.layout.interpreterMode {
	case layoutRuntimeGC:
		offset := c.findGenerationOffset(c.image.runtimeAddress, runtimeRaw)
		if offset < 0 {
			return nil, errors.New("CPython runtime GC generations were not found")
		}
		result := c.generationHeads(c.image.runtimeAddress + uint64(offset))
		return []gcHeads{result}, nil
	case layoutDebugOffsets:
		return c.debugInterpreters(runtimeRaw)
	case layoutFixed:
		return c.fixedInterpreters(runtimeRaw)
	case layoutProbedList:
		// Continue below: older runtimes expose no stable public offsets, so
		// retain structural list discovery as the compatibility fallback.
	default:
		return nil, unsupportedRuntime("CPython interpreter layout is unavailable")
	}
	seen := make(map[uint64]struct{})
	var result []gcHeads
	probes := 0
	for offset := 0; offset+8 <= len(runtimeRaw); offset += 8 {
		candidate := c.image.order.Uint64(runtimeRaw[offset : offset+8])
		for plausiblePtr(candidate) {
			if _, ok := seen[candidate]; ok {
				break
			}
			if probes >= maxInterpreterProbes {
				return nil, fmt.Errorf("CPython interpreter probing exceeds %d attempts",
					maxInterpreterProbes)
			}
			if len(result) >= maxInterpreterCount {
				return nil, fmt.Errorf("CPython interpreter list exceeds %d entries",
					maxInterpreterCount)
			}
			seen[candidate] = struct{}{}
			probes++
			generation, next, generationErr := c.probeInterpreter(candidate)
			if generationErr != nil {
				break
			}
			result = append(result, generation)
			candidate = next
		}
	}
	if len(result) == 0 {
		return nil, errors.New("CPython interpreter GC generations were not found")
	}
	return result, nil
}

func (c *scanner) fixedInterpreters(runtimeRaw []byte) (
	[]gcHeads, error,
) {
	headOffset := c.image.layout.runtimeHeadOffset
	if headOffset+8 > uint64(len(runtimeRaw)) {
		return nil, errors.New("CPython interpreter head is unavailable")
	}
	address := c.image.order.Uint64(
		runtimeRaw[headOffset : headOffset+8])
	if !plausiblePtr(address) {
		return nil, errors.New("CPython interpreter head is invalid")
	}
	return c.interpreterList(address, 0,
		c.image.layout.interpreterGCOffset)
}

func (c *scanner) debugInterpreters(runtimeRaw []byte) (
	[]gcHeads, error,
) {
	if len(runtimeRaw) < debugOffsetsBytes ||
		string(runtimeRaw[:8]) != "xdebugpy" {
		return nil, unsupportedRuntime("CPython debug offsets are unavailable")
	}
	read := func(offset int) uint64 {
		return c.image.order.Uint64(runtimeRaw[offset : offset+8])
	}
	packedVersion := uint32(read(debugVersion))
	if packedVersion == 0 || int(packedVersion>>24) != c.image.version.major ||
		int((packedVersion>>16)&0xff) != c.image.version.minor {
		return nil, unsupportedRuntime(
			"CPython debug offsets version does not match Py_Version")
	}
	if read(debugFreeThreaded) != 0 {
		return nil, fmt.Errorf("%w: free-threaded builds are unsupported",
			errUnsupportedRuntime)
	}
	runtimeHead := read(debugRuntimeHead)
	interpreterSize := read(debugInterpreterSize)
	interpreterNext := read(debugInterpreterNext)
	interpreterGC := read(int(c.image.layout.debugInterpreterGC))
	objectType := read(int(c.image.layout.debugObjectType))
	typeName := read(int(c.image.layout.debugTypeName))
	typeFlags := read(int(c.image.layout.debugTypeFlags))
	if objectType == 0 || typeName == 0 || typeFlags == 0 ||
		objectType&7 != 0 || objectType > 32 ||
		typeName < pyTypeNameOffset || typeName-pyTypeNameOffset > 32 ||
		typeFlags < pyTypeFlagsOffset ||
		typeFlags-pyTypeFlagsOffset != typeName-pyTypeNameOffset {
		return nil, unsupportedRuntime("CPython object debug offsets are invalid")
	}
	runtimeBytes := uint64(len(runtimeRaw))
	gcBytes := gcGenerationsOffset + 3*pyGenerationSize
	if runtimeHead > runtimeBytes-8 || interpreterSize < 8 ||
		interpreterSize > 1<<20 || interpreterNext > interpreterSize-8 ||
		interpreterSize < gcBytes || interpreterGC > interpreterSize-gcBytes {
		return nil, unsupportedRuntime("CPython debug offsets are invalid")
	}
	c.image.layout.objectTypeOffset = objectType
	c.image.layout.objectSizeOffset = objectType + 8
	c.image.layout.typeNameOffset = typeName
	c.image.layout.typeFlagsOffset = typeFlags
	address := c.image.order.Uint64(runtimeRaw[runtimeHead : runtimeHead+8])
	return c.interpreterList(address, interpreterNext, interpreterGC)
}

func (c *scanner) interpreterList(address, nextOffset, gcOffset uint64) (
	[]gcHeads, error,
) {
	seen := make(map[uint64]struct{})
	var result []gcHeads
	for address != 0 {
		if !plausiblePtr(address) {
			return nil, errors.New("CPython interpreter list contains an invalid pointer")
		}
		if len(result) >= maxInterpreterCount {
			return nil, fmt.Errorf("CPython interpreter list exceeds %d entries",
				maxInterpreterCount)
		}
		if _, ok := seen[address]; ok {
			return nil, errors.New("CPython interpreter list contains a cycle")
		}
		seen[address] = struct{}{}
		nextAddress := address + nextOffset
		firstHead := address + gcOffset + gcGenerationsOffset
		if nextAddress < address || firstHead < address {
			return nil, errors.New("CPython interpreter layout overflows")
		}
		nextRaw, err := c.memory.read(nextAddress, 8)
		if err != nil {
			return nil, fmt.Errorf("read CPython interpreter next pointer: %w", err)
		}
		generationRaw, err := c.memory.read(firstHead, int(3*pyGenerationSize))
		if err != nil {
			return nil, fmt.Errorf("read CPython interpreter GC generations: %w", err)
		}
		if c.findGenerationOffset(firstHead, generationRaw) != 0 {
			return nil, errors.New("CPython interpreter GC generations are invalid")
		}
		generation := c.generationHeads(firstHead)
		result = append(result, generation)
		address = c.image.order.Uint64(nextRaw)
	}
	if len(result) == 0 {
		return nil, errors.New("CPython interpreter GC generations were not found")
	}
	return result, nil
}

func (c *scanner) probeInterpreter(address uint64) (gcHeads,
	uint64, error,
) {
	var bestRaw []byte
	bestOffset := -1
	for size := 512; size <= maxInterpreterBytes; size *= 2 {
		raw, err := c.memory.read(address, size)
		if err != nil {
			if bestOffset >= 0 {
				break
			}
			return gcHeads{}, 0, err
		}
		if offset := c.findGenerationOffset(address, raw); offset >= 0 {
			bestRaw = raw
			bestOffset = offset
		}
	}
	if bestOffset < 0 {
		return gcHeads{}, 0,
			errors.New("interpreter has no valid GC generation heads")
	}
	result := c.generationHeads(address + uint64(bestOffset))
	next := c.image.order.Uint64(bestRaw[:8])
	return result, next, nil
}

func (c *scanner) generationHeads(first uint64) gcHeads {
	result := gcHeads{}
	for generation := 0; generation < 3; generation++ {
		result.heads[generation] = first + uint64(generation)*pyGenerationSize
	}
	permanentOffset := 3*pyGenerationSize + 8
	if c.image.version.minor >= 14 {
		permanentOffset = 3 * pyGenerationSize
	}
	result.heads[3] = first + permanentOffset
	return result
}
