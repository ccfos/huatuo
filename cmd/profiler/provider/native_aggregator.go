// Copyright 2025, 2026 The HuaTuo Authors
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

package provider

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// ErrRootRequired indicates the operation requires root privileges.
var ErrRootRequired = errors.New("native profiler requires root privileges")

// requireRoot checks if the current process has root privileges.
func requireRoot() error {
	if os.Geteuid() != 0 {
		return ErrRootRequired
	}
	return nil
}

// Compile-time check: nativeAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*nativeAggregator)(nil)

type nativeAggregator struct {
	profileType  profiling.Type
	mode         profiling.Mode
	containerID  string
	parseOption  *profiler.ParseOption
	sampleType   string
	mu           sync.Mutex
	stackTraces  stackTraceInterner
	stackSamples map[stackSampleKey]int64
	lockSamples  map[string]*lockSample
}

type stackSampleKey struct {
	Process       processKey
	Category      string
	UserTraceID   stackTraceID
	KernelTraceID stackTraceID
}

func newNativeAggregator(cfg *Config) (*nativeAggregator, error) {
	opt, sampleType, err := profileTypeOptions(cfg)
	if err != nil {
		return nil, err
	}
	return &nativeAggregator{
		profileType:  cfg.Type,
		mode:         cfg.Mode,
		containerID:  cfg.ContainerID,
		parseOption:  opt,
		sampleType:   sampleType,
		stackSamples: make(map[stackSampleKey]int64),
		lockSamples:  make(map[string]*lockSample),
	}, nil
}

func (a *nativeAggregator) Aggregate(rec any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch v := rec.(type) {
	case *stackSample:
		key := stackSampleKey{
			Process:       v.Process,
			Category:      v.Category,
			UserTraceID:   a.stackTraces.LookupOrAdd(v.StackTrace.UserFrames),
			KernelTraceID: a.stackTraces.LookupOrAdd(v.StackTrace.KernelFrames),
		}
		a.stackSamples[key] += v.Value

	case *lockSample:
		key := makeLockSampleKey(v)
		if existed, ok := a.lockSamples[key]; ok {
			existed.ContentionCount += v.ContentionCount
			existed.WaitNanoseconds += v.WaitNanoseconds
		} else {
			a.lockSamples[key] = &lockSample{
				Process:         v.Process,
				LockAddress:     v.LockAddress,
				StackTrace:      v.StackTrace,
				WaitNanoseconds: v.WaitNanoseconds,
				ContentionCount: v.ContentionCount,
			}
		}

	default:
		log.Warnf("invalid record type %T, expected *stackSample or *lockSample", rec)
	}
}

func (a *nativeAggregator) Snapshot(context.Context) (*types.ProfilingWindow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.profileType == profiling.TypeLock {
		return a.snapshotLockProfile()
	}
	return a.snapshotCpuMemProfile()
}

func (a *nativeAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.stackTraces.Reset()
	a.stackSamples = make(map[stackSampleKey]int64)
	a.lockSamples = make(map[string]*lockSample)
}

func (a *nativeAggregator) snapshotCpuMemProfile() (*types.ProfilingWindow, error) {
	skipNegForPprof := a.profileType == profiling.TypeMemory &&
		a.mode == profiling.ModePhysicalUsage

	tree := make([]*profiler.TreeItem, 0, len(a.stackSamples))

	for key, value := range a.stackSamples {
		if skipNegForPprof && value < 0 {
			continue
		}

		prefixes := []string{fmt.Sprintf("process %d:%s", key.Process.PID, key.Process.Comm)}
		if key.Category != "" {
			prefixes = append(prefixes, key.Category)
		}
		tree = append(tree, profiler.BuildTreeItem(
			prefixes,
			a.stackTraces.Frames(key.UserTraceID),
			a.stackTraces.Frames(key.KernelTraceID),
			uint64(value),
		))
	}

	return a.buildPprofData(tree)
}

func (a *nativeAggregator) snapshotLockProfile() (*types.ProfilingWindow, error) {
	tree := make([]*profiler.TreeItem, 0, len(a.lockSamples))
	for _, rec := range a.lockSamples {
		prefixes, value := lockPrefixFrames(rec)
		tree = append(tree, profiler.BuildTreeItem(
			prefixes,
			rec.StackTrace.UserFrames,
			rec.StackTrace.KernelFrames,
			value,
		))
	}
	return a.buildPprofData(tree)
}

func makeLockSampleKey(sample *lockSample) string {
	var key strings.Builder
	key.Grow(stackKeyUintSize(sample.LockAddress) + stackFramesKeySize(sample.StackTrace.UserFrames))
	appendStackKeyUint(&key, sample.LockAddress)
	appendStackKeyFrames(&key, sample.StackTrace.UserFrames)
	return key.String()
}

func stackFramesKeySize(frames []string) int {
	size := stackKeyUintSize(uint64(len(frames)))
	for _, frame := range frames {
		size += stackKeyStringSize(frame)
	}
	return size
}

func stackKeyStringSize(value string) int {
	return stackKeyUintSize(uint64(len(value))) + len(value)
}

func stackKeyUintSize(value uint64) int {
	return (bits.Len64(value|1) + 6) / 7
}

func appendStackKeyFrames(key *strings.Builder, frames []string) {
	appendStackKeyUint(key, uint64(len(frames)))
	for _, frame := range frames {
		appendStackKeyString(key, frame)
	}
}

func appendStackKeyString(key *strings.Builder, value string) {
	appendStackKeyUint(key, uint64(len(value)))
	key.WriteString(value)
}

func appendStackKeyUint(key *strings.Builder, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	length := binary.PutUvarint(encoded[:], value)
	key.Write(encoded[:length])
}

// parseCollapsedLine splits a "stack count" folded line into its parts.
// Returns empty strings if the line is malformed.
func parseCollapsedLine(line string) (stack string, count int64, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", 0, false
	}

	idx := strings.LastIndex(line, " ")
	if idx == -1 {
		return "", 0, false
	}

	stack = line[:idx]
	countStr := strings.TrimSpace(line[idx+1:])
	count, err := strconv.ParseInt(countStr, 10, 64)
	if err != nil {
		return "", 0, false
	}

	return stack, count, true
}

// buildPprofData constructs pprof (pyroscope-compatible) profile data.
func (a *nativeAggregator) buildPprofData(tree []*profiler.TreeItem) (*types.ProfilingWindow, error) {
	data, err := profiler.ParseTree(time.Time{}, a.sampleType, tree, a.parseOption)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tree: %w", err)
	}

	return profiler.NewWindow(data, a.containerID)
}

func lockPrefixFrames(rec *lockSample) ([]string, uint64) {
	return []string{
		fmt.Sprintf("lock: %x", rec.LockAddress),
		fmt.Sprintf("PID: %d, COMMAND: %s", rec.Process.PID, rec.Process.Comm),
		fmt.Sprintf("contended count: %d", rec.ContentionCount),
	}, rec.WaitNanoseconds
}

func profileTypeOptions(cfg *Config) (*profiler.ParseOption, string, error) {
	switch cfg.Type {
	case profiling.TypeCPU:
		if cfg.Mode == profiling.ModeOffCPU {
			return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeOffCpuSample, nil
		}
		return &profiler.ParseOption{SampleRate: int64(cfg.Freq)}, profiler.ProfileTypeCpuSample, nil
	case profiling.TypeMemory:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeMemSample, nil
	case profiling.TypeLock:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeLockTimeSample, nil
	default:
		return nil, "", fmt.Errorf("unsupported profile type %q", cfg.Type)
	}
}
