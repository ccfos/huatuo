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
	"errors"
	"fmt"
)

const (
	maxRuntimeProbeBytes = 1024
	maxInterpreterBytes  = 4096
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

type gcHeads struct {
	heads [4]uint64
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
	// Zero marks a rejected probe; positive values identify confirmed list entries.
	seen := make(map[uint64]int)
	var result []gcHeads
	probes := 0
	for offset := 0; offset+8 <= len(runtimeRaw); offset += 8 {
		candidate := c.image.order.Uint64(runtimeRaw[offset : offset+8])
		chainStart := len(result)
		for candidate != 0 {
			confirmed := len(result) > chainStart
			if !plausiblePtr(candidate) {
				if confirmed {
					return nil, errors.New("CPython interpreter list contains an invalid pointer")
				}
				break
			}
			if position, ok := seen[candidate]; ok {
				if confirmed && (position == 0 || position > chainStart) {
					return nil, errors.New("CPython interpreter list contains an invalid or cyclic link")
				}
				break
			}
			if probes >= maxInterpreterProbes {
				return nil, fmt.Errorf("CPython interpreter probing exceeds %d attempts", maxInterpreterProbes)
			}
			if len(result) >= maxInterpreterCount {
				return nil, fmt.Errorf("CPython interpreter list exceeds %d entries", maxInterpreterCount)
			}
			seen[candidate] = 0
			probes++
			generation, next, err := c.probeInterpreter(candidate)
			if err != nil {
				if confirmed {
					return nil, fmt.Errorf("read CPython interpreter successor: %w", err)
				}
				break
			}
			result = append(result, generation)
			seen[candidate] = len(result)
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
		runtimeRaw[headOffset : headOffset+8],
	)
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
			"CPython debug offsets version does not match Py_Version",
		)
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
		// Debian builds can align PyGC_Head to four bytes instead of eight.
		// Probe only the two known positions and validate all three lists.
		firstHead := address + gcOffset + gcGenerationsOffset - 4
		if nextAddress < address || firstHead < address {
			return nil, errors.New("CPython interpreter layout overflows")
		}
		nextRaw, err := c.memory.read(nextAddress, 8)
		if err != nil {
			return nil, fmt.Errorf("read CPython interpreter next pointer: %w", err)
		}
		generationRaw, err := c.memory.read(firstHead, int(3*pyGenerationSize)+4)
		if err != nil {
			return nil, fmt.Errorf("read CPython interpreter GC generations: %w", err)
		}
		offset := c.findGenerationOffset(firstHead, generationRaw)
		if offset < 0 {
			return nil, errors.New("CPython interpreter GC generations are invalid")
		}
		generation := c.generationHeads(firstHead + uint64(offset))
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
	permanent := first + 3*pyGenerationSize
	if c.image.version.minor < 14 {
		// The intervening generation0 pointer retains eight-byte alignment
		// even when the generation heads are only four-byte aligned.
		permanent = alignUp(permanent, 8) + 8
	}
	result.heads[3] = permanent
	return result
}
