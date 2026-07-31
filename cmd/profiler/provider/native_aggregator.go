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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/pkg/profiling"
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
	aggrMap          map[string]*stackSample
	lockAggrMap      map[string]*lockSample
	isLockFoldedDone bool
}

func newNativeAggregator(pctx *pcontext.ProfilerContext) (*nativeAggregator, error) {
	f, err := aggregator.NewFormatterForOutput(pctx)
	if err != nil {
		return nil, err
	}

	return &nativeAggregator{
		formatter:   f,
		aggrMap:     make(map[string]*stackSample),
		lockAggrMap: make(map[string]*lockSample),
	}, nil
}

func (a *nativeAggregator) Aggregate(rec any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch v := rec.(type) {
	case *stackSample:
		key := fmt.Sprintf(
			"%d\x00%s\x00%s\x00%s",
			v.Process.PID,
			v.Process.Comm,
			v.UserStack,
			v.KernelStack,
		)

		if existed, ok := a.aggrMap[key]; ok {
			existed.Value += v.Value
		} else {
			a.aggrMap[key] = &stackSample{
				Process:     v.Process,
				UserStack:   v.UserStack,
				KernelStack: v.KernelStack,
				Value:       v.Value,
			}
		}

		log.Debugf("aggregate: pid=%d comm=%s samples=%d key=%q", v.Process.PID, v.Process.Comm, v.Value, key)

		if a.formatter != nil {
			frames := []string{
				fmt.Sprintf("process %d:%s", v.Process.PID, v.Process.Comm),
			}
			frames = appendStackFrames(frames, v.UserStack, v.KernelStack)
			log.Debugf("formatter add: frames=%v count=%d", frames, v.Value)
			if err := a.formatter.Add(&output.Sample{Frames: frames, Count: v.Value}); err != nil {
				log.Warnf("formatter add sample: %v", err)
			}
		}

	case *lockSample:
		key := fmt.Sprintf("%s\x00%d", v.UserStack, v.LockAddress)
		if existed, ok := a.lockAggrMap[key]; ok {
			existed.ContentionCount += v.ContentionCount
			existed.WaitNanoseconds += v.WaitNanoseconds
		} else {
			a.lockAggrMap[key] = &lockSample{
				Process:         v.Process,
				LockAddress:     v.LockAddress,
				UserStack:       v.UserStack,
				KernelStack:     v.KernelStack,
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

	a.aggrMap = make(map[string]*stackSample)
	a.lockAggrMap = make(map[string]*lockSample)
	a.isLockFoldedDone = false
}

func (a *nativeAggregator) OutputFormatter() output.Formatter {
	if a.formatter != nil && !a.isLockFoldedDone && len(a.lockAggrMap) > 0 {
		a.buildLockFolded()
		a.isLockFoldedDone = true
	}
	return a.formatter
}

func (a *nativeAggregator) buildLockFolded() {
	for _, rec := range a.lockAggrMap {
		frames, value := lockPrefixFrames(rec)
		frames = appendStackFrames(frames, rec.UserStack, rec.KernelStack)
		if err := a.formatter.Add(&output.Sample{Frames: frames, Count: int64(value)}); err != nil {
			log.Warnf("formatter add lock sample: %v", err)
		}
	}
}

func (a *nativeAggregator) snapshotCpuMemProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.aggrMap) == 0 {
		return nil, nil
	}

	skipNegForPprof := pctx.Type == profiling.TypeMemory &&
		pctx.MemoryMode == profiling.MemoryModePhysicalUsage

	tree := make([]*profiler.TreeItem, 0, len(a.aggrMap))

	for _, rec := range a.aggrMap {
		if skipNegForPprof && rec.Value < 0 {
			continue
		}

		prefixes := []string{fmt.Sprintf("process %d:%s", rec.Process.PID, rec.Process.Comm)}
		item := buildTreeItem(prefixes, rec.UserStack, rec.KernelStack, uint64(rec.Value))
		tree = append(tree, item)
	}

	return buildPprofData(pctx, tree)
}

func (a *nativeAggregator) snapshotLockProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.lockAggrMap) == 0 {
		return nil, nil
	}

	tree := make([]*profiler.TreeItem, 0, len(a.lockAggrMap))
	for _, rec := range a.lockAggrMap {
		prefixes, value := lockPrefixFrames(rec)
		tree = append(tree, buildTreeItem(prefixes, rec.UserStack, rec.KernelStack, value))
	}
	return buildPprofData(pctx, tree)
}

func appendStackFrames(frames []string, userStack, kernelStack string) []string {
	u := strings.TrimSuffix(userStack, ";")
	k := strings.TrimSuffix(kernelStack, ";")

	for _, s := range strings.Split(u, ";") {
		if s != "" {
			frames = append(frames, s)
		}
	}

	for _, s := range strings.Split(k, ";") {
		if s != "" {
			frames = append(frames, s)
		}
	}

	return frames
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

func buildTreeItem(prefixes []string, userStack, kernelStack string, value uint64) *profiler.TreeItem {
	ustacks := strings.Split(userStack, ";")
	kstacks := strings.Split(kernelStack, ";")

	stackLen := len(prefixes) + len(ustacks) + len(kstacks)
	stack := make([][]byte, 0, stackLen)

	for _, p := range prefixes {
		stack = append(stack, []byte(p))
	}

	for _, s := range ustacks {
		if s != "" {
			stack = append(stack, []byte(s))
		}
	}

	for _, s := range kstacks {
		if s != "" {
			stack = append(stack, []byte(s))
		}
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
		return &profiler.ParseOption{SampleRate: int64(pctx.Freq)}, profiler.ProfileTypeCpuSample, nil
	case profiling.TypeMemory:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeMemSample, nil
	case profiling.TypeLock:
		return &profiler.ParseOption{SampleRate: profiler.NoSampleRate}, profiler.ProfileTypeLockTimeSample, nil
	default:
		return nil, "", fmt.Errorf("unsupported profile type %q", pctx.Type)
	}
}
