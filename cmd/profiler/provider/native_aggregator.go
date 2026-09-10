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
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/pkg/profiling"
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
	mu sync.Mutex

	formatter        output.Formatter
	stackTraces      stackTraceInterner
	stackSamples     map[stackSampleKey]int64
	lockSamples      map[string]*lockSample
	isLockFoldedDone bool
}

type stackSampleKey struct {
	Process       processKey
	Category      string
	UserTraceID   stackTraceID
	KernelTraceID stackTraceID
}

func newNativeAggregator(pctx *pcontext.ProfilerContext) (*nativeAggregator, error) {
	f, err := aggregator.NewFormatterForOutput(pctx)
	if err != nil {
		return nil, err
	}

	return &nativeAggregator{
		formatter:    f,
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

		if a.formatter != nil {
			frameCount := 1 + v.StackTrace.frameCount()
			if v.Category != "" {
				frameCount++
			}
			frames := make([]string, 0, frameCount)
			frames = append(frames, fmt.Sprintf("process %d:%s", v.Process.PID, v.Process.Comm))
			if v.Category != "" {
				frames = append(frames, v.Category)
			}
			frames = v.StackTrace.appendTo(frames)
			log.Debugf("formatter add: frames=%v count=%d", frames, v.Value)
			if err := a.formatter.Add(&output.Sample{Frames: frames, Count: v.Value}); err != nil {
				log.Warnf("formatter add sample: %v", err)
			}
		}

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

func (a *nativeAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !pctx.OutputFormat.IsUpload() {
		return nil, nil
	}

	if pctx.Type == profiling.TypeLock {
		return a.snapshotLockProfile(pctx)
	}
	return a.snapshotCpuMemProfile(pctx)
}

func (a *nativeAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.formatter != nil {
		a.formatter.Reset()
	}

	a.stackTraces.Reset()
	a.stackSamples = make(map[stackSampleKey]int64)
	a.lockSamples = make(map[string]*lockSample)
	a.isLockFoldedDone = false
}

func (a *nativeAggregator) OutputFormatter() output.Formatter {
	if a.formatter != nil && !a.isLockFoldedDone && len(a.lockSamples) > 0 {
		a.buildLockFolded()
		a.isLockFoldedDone = true
	}
	return a.formatter
}

func (a *nativeAggregator) buildLockFolded() {
	for _, rec := range a.lockSamples {
		frames, value := lockPrefixFrames(rec)
		frames = rec.StackTrace.appendTo(frames)
		if err := a.formatter.Add(&output.Sample{Frames: frames, Count: int64(value)}); err != nil {
			log.Warnf("formatter add lock sample: %v", err)
		}
	}
}

func (a *nativeAggregator) snapshotCpuMemProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.stackSamples) == 0 {
		return nil, nil
	}

	skipNegForPprof := pctx.Type == profiling.TypeMemory &&
		pctx.Mode == profiling.ModePhysicalUsage

	tree := make([]*profiler.TreeItem, 0, len(a.stackSamples))

	for key, value := range a.stackSamples {
		if skipNegForPprof && value < 0 {
			continue
		}

		prefixes := []string{fmt.Sprintf("process %d:%s", key.Process.PID, key.Process.Comm)}
		if key.Category != "" {
			prefixes = append(prefixes, key.Category)
		}
		trace := symbolizedStackTrace{
			UserFrames:   a.stackTraces.Frames(key.UserTraceID),
			KernelFrames: a.stackTraces.Frames(key.KernelTraceID),
		}
		item := buildTreeItem(prefixes, trace, uint64(value))
		tree = append(tree, item)
	}

	return buildPprofData(pctx, tree)
}

func (a *nativeAggregator) snapshotLockProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.lockSamples) == 0 {
		return nil, nil
	}

	tree := make([]*profiler.TreeItem, 0, len(a.lockSamples))
	for _, rec := range a.lockSamples {
		prefixes, value := lockPrefixFrames(rec)
		tree = append(tree, buildTreeItem(prefixes, rec.StackTrace, value))
	}
	return buildPprofData(pctx, tree)
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

func buildTreeItem(prefixes []string, trace symbolizedStackTrace, value uint64) *profiler.TreeItem {
	stackLen := len(prefixes) + trace.frameCount()
	stack := make([][]byte, 0, stackLen)

	for _, p := range prefixes {
		stack = append(stack, []byte(p))
	}

	for _, frame := range trace.UserFrames {
		stack = append(stack, []byte(frame))
	}

	for _, frame := range trace.KernelFrames {
		stack = append(stack, []byte(frame))
	}

	return &profiler.TreeItem{
		Stack: stack,
		Value: value,
	}
}

// buildPprofData constructs pprof (pyroscope-compatible) profile data.
func buildPprofData(pctx *pcontext.ProfilerContext, tree []*profiler.TreeItem) (*profiler.ProfileData, error) {
	opt, sampleType, err := profileTypeOptions(pctx)
	if err != nil {
		return nil, err
	}

	data, err := profiler.ParseTree(time.Now(), sampleType, tree, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tree: %w", err)
	}

	return data, nil
}

func lockPrefixFrames(rec *lockSample) ([]string, uint64) {
	return []string{
		fmt.Sprintf("lock: %x", rec.LockAddress),
		fmt.Sprintf("PID: %d, COMMAND: %s", rec.Process.PID, rec.Process.Comm),
		fmt.Sprintf("contended count: %d", rec.ContentionCount),
	}, rec.WaitNanoseconds
}

func profileTypeOptions(pctx *pcontext.ProfilerContext) (*profiler.ParseOption, string, error) {
	switch pctx.Type {
	case profiling.TypeCPU:
		if pctx.Mode == profiling.ModeOffCPU {
			return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeOffCpuSample, nil
		}
		return &profiler.ParseOption{SampleRate: int64(pctx.Freq)}, profiler.ProfileTypeCpuSample, nil
	case profiling.TypeMemory:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeMemSample, nil
	case profiling.TypeLock:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeLockTimeSample, nil
	default:
		return nil, "", fmt.Errorf("unsupported profile type %q", pctx.Type)
	}
}
