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
	"fmt"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

// Compile-time check: nativeAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*nativeAggregator)(nil)

// processIDName identifies a process during aggregation.
type processIDName struct {
	Pid  uint32
	Name string
}

// stackEntry represents a single sampling record (not aggregated).
type stackEntry struct {
	Proc    *processIDName
	User    string
	Kernel  string
	Samples int64
}

// aggrKey is used as the map key for aggregation.
type aggrKey struct {
	_pid  uint32
	_name string
	_u    string
	_k    string
}

type processIDNameLock struct {
	Pid  uint32
	Name string
	Lock uint64
}

type lockStackEntry struct {
	Proc      *processIDNameLock
	User      string
	Kernel    string
	WaitTime  uint64
	Contended uint32
}

type lockAggrKey struct {
	_u    string
	_lock uint64
}

type nativeAggregator struct {
	mu sync.Mutex

	formatter    output.Formatter
	aggrMap      map[aggrKey]*stackEntry
	lockAggrMap  map[lockAggrKey]*lockStackEntry
}

func newNativeAggregator(pctx *pcontext.ProfilerContext) (*nativeAggregator, error) {
	f, err := pctx.OutputFormat.NewFormatter()
	if err != nil {
		return nil, err
	}

	return &nativeAggregator{
		formatter:    f,
		aggrMap:      make(map[aggrKey]*stackEntry),
		lockAggrMap:  make(map[lockAggrKey]*lockStackEntry),
	}, nil
}

func (a *nativeAggregator) Aggregate(rec any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch v := rec.(type) {
	case *stackEntry:
		key := aggrKey{v.Proc.Pid, v.Proc.Name, v.User, v.Kernel}

		if existed, ok := a.aggrMap[key]; ok {
			existed.Samples += v.Samples
		} else {
			a.aggrMap[key] = &stackEntry{
				Proc:    v.Proc,
				User:    v.User,
				Kernel:  v.Kernel,
				Samples: v.Samples,
			}
		}

		frames := []string{
			fmt.Sprintf("process %d:%s", v.Proc.Pid, v.Proc.Name),
		}
		frames = appendStackFrames(frames, v.User, v.Kernel)
		_ = a.formatter.Add(&output.Sample{Frames: frames, Count: v.Samples})

	case *lockStackEntry:
		key := lockAggrKey{
			_u:    v.User,
			_lock: v.Proc.Lock,
		}

		if existed, ok := a.lockAggrMap[key]; ok {
			existed.Contended += v.Contended
			existed.WaitTime += v.WaitTime
		} else {
			a.lockAggrMap[key] = &lockStackEntry{
				Proc:      v.Proc,
				User:      v.User,
				Kernel:    v.Kernel,
				WaitTime:  v.WaitTime,
				Contended: v.Contended,
			}
		}

	default:
		log.Infof("invalid record type %T, expected *stackEntry or *lockStackEntry", rec)
	}
}

func (a *nativeAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if pctx.Type == "lock" {
		a.buildLockFolded(pctx)
	}

	if !pctx.OutputFormat.IsUpload() {
		return nil, nil
	}

	if pctx.Type == "lock" {
		return a.snapshotLockProfile(pctx)
	}

	return a.snapshotCpuMemProfile(pctx)
}

func (a *nativeAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.formatter.Reset()
	a.aggrMap = make(map[aggrKey]*stackEntry)
	a.lockAggrMap = make(map[lockAggrKey]*lockStackEntry)
}

func (a *nativeAggregator) OutputFormatter() output.Formatter {
	return a.formatter
}

// buildLockFolded populates the folded formatter with lock profile entries.
// Lock stacks require mode-dependent frame construction, so they are
// materialized at snapshot time rather than during Aggregate.
func (a *nativeAggregator) buildLockFolded(pctx *pcontext.ProfilerContext) {
	if len(a.lockAggrMap) == 0 {
		return
	}

	outputType := pctx.ExtraFlags["mode"]

	for _, rec := range a.lockAggrMap {
		frames := []string{
			fmt.Sprintf("lock: %x", rec.Proc.Lock),
			fmt.Sprintf("PID: %d, COMMAND: %s", rec.Proc.Pid, rec.Proc.Name),
		}

		val := rec.WaitTime

		if outputType == "" {
			frames = append(frames, fmt.Sprintf("contended count: %d", rec.Contended))
		} else {
			if outputType == "count" {
				val = uint64(rec.Contended)
			}
		}

		frames = appendStackFrames(frames, rec.User, rec.Kernel)
		_ = a.formatter.Add(&output.Sample{Frames: frames, Count: int64(val)})
	}
}

func (a *nativeAggregator) snapshotCpuMemProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.aggrMap) == 0 {
		return nil, nil
	}

	skipNegForPprof := pctx.Type == "mem" &&
		pctx.ExtraFlags["mode"] == "native_physical_usage"

	tree := make([]*profiler.TreeItem, 0, len(a.aggrMap))

	for _, rec := range a.aggrMap {
		if skipNegForPprof && rec.Samples < 0 {
			continue
		}

		prefixes := []string{fmt.Sprintf("process %d:%s", rec.Proc.Pid, rec.Proc.Name)}
		item := buildTreeItem(prefixes, rec.User, rec.Kernel, uint64(rec.Samples))
		tree = append(tree, item)
	}

	return buildPprofData(pctx, tree)
}

func (a *nativeAggregator) snapshotLockProfile(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.lockAggrMap) == 0 {
		return nil, nil
	}

	tree := make([]*profiler.TreeItem, 0, len(a.lockAggrMap))
	outputType := pctx.ExtraFlags["mode"]

	for _, rec := range a.lockAggrMap {
		prefixes := []string{
			fmt.Sprintf("lock :%x", rec.Proc.Lock),
			fmt.Sprintf("PID: %d: COMMAND: %s", rec.Proc.Pid, rec.Proc.Name),
		}

		val := rec.WaitTime

		if outputType == "" {
			prefixes = append(prefixes, fmt.Sprintf("contended count: %d", rec.Contended))
		} else {
			if outputType == "count" {
				val = uint64(rec.Contended)
			}
		}

		item := buildTreeItem(prefixes, rec.User, rec.Kernel, val)
		tree = append(tree, item)
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
	var (
		opt        *profiler.ParseOption
		sampleType string
	)

	switch pctx.Type {
	case "cpu":
		opt = &profiler.ParseOption{SampleRate: int64(pctx.Freq)}
		sampleType = profiler.ProfileTypeCpuSample

	case "mem":
		opt = &profiler.ParseOption{SampleRate: profiler.NoSampleRate}
		sampleType = profiler.ProfileTypeMemSample

	case "lock":
		opt = &profiler.ParseOption{SampleRate: profiler.NoSampleRate}

		mode := pctx.ExtraFlags["mode"]
		sampleType = profiler.ProfileTypeLockTimeSample
		if mode == "count" {
			sampleType = profiler.ProfileTypeLockCountSample
		}

	default:
		return nil, fmt.Errorf("unsupported profile type: %s", pctx.Type)
	}

	data, err := profiler.ParseTree(time.Now(), sampleType, tree, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tree: %w", err)
	}

	return data, nil
}
