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

package java

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	// Leave time to reduce the copied sample into the final histogram.
	resultBuildReserve = 15 * time.Millisecond
)

var errHotSpotUnavailable = errors.New("HotSpot external heap scan is unsupported")

type sampleStats struct {
	ordinarySampledBytes uint64
	ordinary             map[uint64]classSample
	humongous            map[uint64]classSample
}

func unsupportedHotSpot(reason string) error {
	return fmt.Errorf("%w: %s", errHotSpotUnavailable, reason)
}

// Capture samples the running HotSpot heap and reduces it to type aggregates.
func capture(ctx context.Context,
	identity memsnap.ProcessIdentity, maxEntries int,
	samplingNonce uint64,
) (*memsnap.Snapshot, error) {
	readPID := identity.TGID
	if readPID <= 0 || maxEntries <= 0 {
		return nil, errors.New("HotSpot external scan limits are invalid")
	}
	scanDeadline, hasDeadline := memsnap.DeadlineWithReserve(
		ctx, resultBuildReserve)
	memory := processMemory{
		pid: readPID, ctx: ctx, deadline: scanDeadline, hasDeadline: hasDeadline,
	}
	if err := memsnap.ValidateIdentity(procRoot, identity); err != nil {
		return nil, err
	}
	metadata, err := loadMetadata(procRoot, memory)
	if err != nil {
		return nil, err
	}
	snapshot := &memsnap.Snapshot{RuntimeVersion: metadata.image.displayVersion()}
	gcBefore, trackGC, gcErr := readGCSequence(memory, metadata)
	if gcErr != nil {
		appendPartial(snapshot, "HotSpot GC sequence could not be read before scanning")
		trackGC = false
	}
	if err := memsnap.ValidateIdentity(procRoot, identity); err != nil {
		return nil, err
	}
	regions, err := readRegions(memory, metadata)
	if err != nil {
		return nil, err
	}
	heap, err := groupRegions(regions, metadata)
	if err != nil {
		return nil, err
	}
	mirrorOopSizeOffset, err := mirrorSizeOffset(memory, metadata)
	if err != nil {
		return nil, err
	}
	encoding, err := pointerEncoding(memory, metadata)
	if err != nil {
		return nil, err
	}
	if err := memsnap.ValidateIdentity(procRoot, identity); err != nil {
		return nil, err
	}
	classes := make(map[uint64]*klass)
	statistics := sampleStats{
		ordinary:  make(map[uint64]classSample),
		humongous: make(map[uint64]classSample),
	}
	failedHumongousGroups, firstInvalidReason := scanHumongous(memory,
		metadata, heap.humongous, encoding, mirrorOopSizeOffset, classes,
		statistics.humongous, snapshot)
	scanOrdinary(memory, metadata, heap.ordinary, encoding, mirrorOopSizeOffset,
		samplingNonce, heap.ordinaryUsed, classes, &statistics, snapshot)
	if metadata.klassLimitReached {
		appendPartial(snapshot, "HotSpot Klass cache safety limit reached")
	}
	if trackGC {
		gcAfter, _, readErr := readGCSequence(memory, metadata)
		if readErr != nil {
			appendPartial(snapshot,
				"HotSpot GC sequence could not be read after scanning")
		} else if gcAfter != gcBefore {
			appendPartial(snapshot,
				"HotSpot GC ran during the external heap scan; values may span heap states")
		}
	}
	finishStatus(snapshot, failedHumongousGroups, heap.ordinaryUsed,
		statistics.ordinarySampledBytes)
	aggregates := estimateAggregates(classes, statistics, heap.ordinaryUsed)
	if len(aggregates) == 0 {
		deadlineStopped := memory.check() != nil
		identityErr := memsnap.ValidateIdentity(procRoot, identity)
		targetExited := errors.Is(identityErr, os.ErrNotExist)
		if identityErr != nil && !targetExited {
			return nil, identityErr
		}
		if deadlineStopped || targetExited || metadata.klassLimitReached {
			appendPartial(snapshot,
				"no valid Java objects were classified before capture stopped")
		} else {
			if firstInvalidReason == nil {
				firstInvalidReason = errors.New("scan stopped before the first valid object")
			}
			return nil, fmt.Errorf(
				"external HotSpot scan found no valid Java objects; first rejected candidate: %w",
				firstInvalidReason)
		}
	}
	objects := make([]memsnap.ObjectAggregate, 0, len(aggregates))
	for _, aggregate := range aggregates {
		if aggregate.Count != 0 {
			aggregate.AverageBytes = float64(aggregate.ShallowBytes) /
				float64(aggregate.Count)
		}
		objects = append(objects, aggregate)
	}
	sortObjects(objects)
	if len(objects) > maxEntries {
		objects = objects[:maxEntries]
		snapshot.OutputTruncated = true
	}
	snapshot.Entries = memsnap.EntriesFromObjects(objects)
	if err := memsnap.ValidateIdentity(procRoot, identity); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		appendPartial(snapshot,
			"target exited before final process identity validation")
	}
	return snapshot, nil
}

func scanHumongous(memory processMemory, metadata *vmMeta, groups []regionGroup,
	encoding ptrEncoding, mirrorOopSizeOffset int, classes map[uint64]*klass,
	samples map[uint64]classSample, snapshot *memsnap.Snapshot,
) (int, error) {
	failedGroups := 0
	var firstInvalid error
	for _, group := range groups {
		if len(group.regions) == 0 {
			continue
		}
		region := group.regions[0]
		groupBytes := humongousGroupBytes(group)
		if memory.check() != nil {
			appendPartial(snapshot,
				"deadline reached while scanning HotSpot humongous objects")
			break
		}
		readBytes := min(region.top-region.bottom, encoding.headerBytes())
		raw, readErr := memory.read(region.bottom, int(readBytes))
		if readErr == nil {
			klassAddress, validKlass := encoding.klassAddress(raw)
			if !validKlass {
				readErr = fmt.Errorf("humongous object %#x Klass is invalid",
					region.bottom)
			} else {
				klass := classes[klassAddress]
				if klass == nil {
					klass, readErr = readKlass(memory, metadata, klassAddress)
					if readErr == nil && !metadata.cacheKlass(classes, klassAddress, klass) {
						break
					}
				}
				objectBytes := uint64(0)
				if readErr == nil {
					objectBytes, readErr = humongousObjectSize(memory,
						region.bottom, raw, klass, metadata,
						mirrorOopSizeOffset, encoding.headerBytes())
				}
				if readErr == nil && (objectBytes == 0 ||
					objectBytes > maxJavaObjectBytes || objectBytes > groupBytes) {
					readErr = fmt.Errorf("humongous object %#x size %d is invalid",
						region.bottom, objectBytes)
				}
				if readErr == nil {
					addClassSample(samples, klassAddress, 1, objectBytes)
				}
			}
		}
		if readErr != nil {
			failedGroups++
			if firstInvalid == nil {
				firstInvalid = readErr
			}
		}
	}
	return failedGroups, firstInvalid
}

func scanOrdinary(memory processMemory, metadata *vmMeta, regions []region,
	encoding ptrEncoding, mirrorOopSizeOffset int, seed, ordinaryUsed uint64,
	classes map[uint64]*klass, statistics *sampleStats,
	snapshot *memsnap.Snapshot,
) {
	weightedBudget := min(ordinaryUsed, uint64(maxSampleBytes))
	attempted := make(map[uint64]struct{})
	sampledBytes := uint64(0)
	attempts := 0
	reason := scanWindows(memory, regions, seed,
		weightedBudget, windowBytes,
		func(batch []sampleWindow) bool {
			batchBytes := uint64(0)
			for _, sample := range batch {
				batchBytes = memsnap.SaturatingAdd(batchBytes, uint64(len(sample.raw)))
			}
			sampledBytes = memsnap.SaturatingAdd(sampledBytes, batchBytes)
			target := maxKlassAttempts
			if sampledBytes < weightedBudget {
				target = int((uint64(maxKlassAttempts)*sampledBytes +
					weightedBudget - 1) / weightedBudget)
			}
			used, completed := resolveBatchKlasses(memory, metadata, batch,
				classes, attempted, encoding, target-attempts)
			attempts += used
			if !completed {
				return false
			}
			for _, sample := range batch {
				statistics.ordinarySampledBytes = memsnap.SaturatingAdd(
					statistics.ordinarySampledBytes, uint64(len(sample.raw)))
				scanKnownWindow(sample, classes, encoding, metadata,
					mirrorOopSizeOffset, statistics.ordinary)
			}
			return memory.check() == nil
		})
	if reason != "" {
		appendPartial(snapshot, "HotSpot ordinary heap sampling: "+reason)
	}
}

func humongousGroupBytes(group regionGroup) uint64 {
	if len(group.regions) == 0 {
		return 0
	}
	bottom := group.regions[0].bottom
	groupTop := bottom
	for _, region := range group.regions {
		regionTop := region.bottom + region.capacity
		if regionTop > groupTop {
			groupTop = regionTop
		}
	}
	return groupTop - bottom
}

// finishStatus records sampling loss. External scanning never stops the VM, so
// even a full byte scan remains partial while the heap may change concurrently.
func finishStatus(snapshot *memsnap.Snapshot, failedHumongousGroups int,
	ordinaryUsed, ordinarySampled uint64,
) {
	if failedHumongousGroups != 0 {
		appendPartial(snapshot, fmt.Sprintf(
			"%d HotSpot humongous object groups could not be validated",
			failedHumongousGroups))
	}
	if ordinarySampled < ordinaryUsed {
		appendPartial(snapshot, fmt.Sprintf(
			"bounded HotSpot sample scanned %d of %d ordinary used bytes; ordinary values are estimates",
			ordinarySampled, ordinaryUsed))
	}
	appendPartial(snapshot,
		"external HotSpot scan ran concurrently with the target VM")
}

func appendPartial(snapshot *memsnap.Snapshot, reason string) {
	if snapshot == nil || reason == "" {
		return
	}
	snapshot.Status = memsnap.StatusPartial
	if snapshot.Reason == "" {
		snapshot.Reason = reason
		return
	}
	snapshot.Reason += "; " + reason
}
