// Copyright 2025 The HuaTuo Authors
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

package callback

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	context "huatuo-bamai/internal/profiler/context"
)

// ------------------------
// CPU / MEM stack struct
// ------------------------
// ProcessIDName is used to identify a process during aggregation
type ProcessIDName struct {
	Pid  uint32
	Name string
}

// StackEntry represents a single sampling record (not aggregated)
type StackEntry struct {
	Proc    *ProcessIDName
	User    string
	Kernel  string
	Samples int64
}

// aggrKey is used as the map key for aggregation
type aggrKey struct {
	_pid  uint32
	_name string
	_u    string
	_k    string
}

// -------------------
// LOCK stack struct
// -------------------
type ProcessIDNameLock struct {
	Pid  uint32
	Name string
	Lock uint64
}

type LockStackEntry struct {
	Proc      *ProcessIDNameLock
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
	*aggregator.Aggregator

	aggrMap     map[aggrKey]*StackEntry
	lockAggrMap map[lockAggrKey]*LockStackEntry
}

func NewNativeAggregator(pctx *context.ProfilerContext) *nativeAggregator {
	aggr := &nativeAggregator{}
	aggr.Aggregator = aggregator.NewAggregator(pctx, aggr.RecordProcessor, aggr.AggregatedExporter)

	aggr.aggrMap = make(map[aggrKey]*StackEntry)
	aggr.lockAggrMap = make(map[lockAggrKey]*LockStackEntry)

	return aggr
}

func (a *nativeAggregator) RecordProcessor(rec any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// determine the type of rec
	switch v := rec.(type) {
	case *StackEntry:
		key := aggrKey{v.Proc.Pid, v.Proc.Name, v.User, v.Kernel}
		if existed, ok := a.aggrMap[key]; ok {
			existed.Samples += v.Samples
		} else {
			a.aggrMap[key] = &StackEntry{
				Proc:    v.Proc,
				User:    v.User,
				Kernel:  v.Kernel,
				Samples: v.Samples,
			}
		}

	case *LockStackEntry:
		key := lockAggrKey{
			_u:    v.User,
			_lock: v.Proc.Lock,
		}

		if existed, ok := a.lockAggrMap[key]; ok {
			existed.Contended += v.Contended
			existed.WaitTime += v.WaitTime
		} else {
			a.lockAggrMap[key] = &LockStackEntry{
				Proc:      v.Proc,
				User:      v.User,
				Kernel:    v.Kernel,
				WaitTime:  v.WaitTime,
				Contended: v.Contended,
			}
		}

	default:
		log.Infof("invalid record")
	}
}

func (a *nativeAggregator) AggregatedExporter(pctx *context.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var res any
	var err error
	if pctx.Type == "lock" {
		res, err = a.exportLockProfile(pctx)
	} else {
		res, err = a.exportCpuMemProfile(pctx)
	}

	// reset clears all aggregated data after each export window
	a.reset()

	return res, err
}

func (a *nativeAggregator) exportCpuMemProfile(pctx *context.ProfilerContext) (any, error) {
	if len(a.aggrMap) == 0 {
		log.Infof("There is no data in aggregate queue")
		return nil, nil
	}

	foldedData := &strings.Builder{}
	tree := make([]*profiler.TreeItem, 0, len(a.aggrMap))
	skipNegForPprof := pctx.Type == "mem" &&
		pctx.ExtraFlags["mode"] == "native_physical_usage" &&
		(pctx.OutputFormat == "pprof" || pctx.OutputFormat == "es")

	for _, rec := range a.aggrMap {
		stackStr := mergeStackTraces(rec.User, rec.Kernel)

		fmt.Fprintf(
			foldedData,
			"process %d:%q;%s %d\n",
			rec.Proc.Pid,
			rec.Proc.Name,
			stackStr,
			rec.Samples,
		)

		prefixes := []string{fmt.Sprintf("process %d:%s", rec.Proc.Pid, rec.Proc.Name)}
		if !skipNegForPprof || rec.Samples >= 0 {
			item := buildTreeItem(prefixes, rec.User, rec.Kernel, uint64(rec.Samples))
			tree = append(tree, item)
		}
	}

	switch pctx.OutputFormat {
	case "pprof", "es":
		return buildPprofData(pctx, tree)
	default:
		return []byte(foldedData.String()), nil
	}
}

func (a *nativeAggregator) exportLockProfile(pctx *context.ProfilerContext) (any, error) {
	if len(a.lockAggrMap) == 0 {
		log.Infof("There is no data in aggregate queue")
		return nil, nil
	}

	foldedData := &strings.Builder{}
	tree := make([]*profiler.TreeItem, 0, len(a.lockAggrMap))
	outputType := pctx.ExtraFlags["mode"]
	for _, rec := range a.lockAggrMap {
		stackStr := mergeStackTraces(rec.User, rec.Kernel)

		prefixes := []string{
			fmt.Sprintf("lock :%x", rec.Proc.Lock),
			fmt.Sprintf("PID: %d: COMMAND: %s", rec.Proc.Pid, rec.Proc.Name),
		}

		var item *profiler.TreeItem
		val := rec.WaitTime
		if outputType == "" {
			// default for mixed mode:
			// collects both contended count and wait time metrics
			fmt.Fprintf(
				foldedData,
				"lock: %x;PID: %d, COMMAND: %s;contended count: %d;%s %d\n",
				rec.Proc.Lock,
				rec.Proc.Pid,
				rec.Proc.Name,
				rec.Contended,
				stackStr,
				val,
			)
			prefixes = append(prefixes, fmt.Sprintf("contended count: %d", rec.Contended))
		} else {
			// If --flags type is specified, support collecting either:
			// - Contended count: number of lock acquisition attempts
			// - Wait time: threads spent waiting for contended locks
			if outputType == "count" {
				val = uint64(rec.Contended)
			}
			fmt.Fprintf(
				foldedData,
				"lock: %x;PID: %d, COMMAND: %s;%s %d\n",
				rec.Proc.Lock,
				rec.Proc.Pid,
				rec.Proc.Name,
				stackStr,
				val,
			)
		}
		item = buildTreeItem(prefixes, rec.User, rec.Kernel, val)
		tree = append(tree, item)
	}

	switch pctx.OutputFormat {
	case "pprof", "es":
		return buildPprofData(pctx, tree)
	default:
		return []byte(foldedData.String()), nil
	}
}

func mergeStackTraces(u, k string) string {
	u = strings.TrimSuffix(u, ";")
	k = strings.TrimSuffix(k, ";")

	switch {
	case u != "" && k != "":
		return u + ";" + k
	case u != "":
		return u
	default:
		return k
	}
}

func buildTreeItem(prefixes []string, userStack, kernelStack string, value uint64) *profiler.TreeItem {
	ustacks := strings.Split(userStack, ";")
	kstacks := strings.Split(kernelStack, ";")

	// Pre-allocate capacity for: prefix entries (which identify the process or lock) + user stack frames + kernel stack frames
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
		/*
		 * In Native Memory Mode:
		 * Retained mem (native_physical_usage) emits +/- deltas per stack.
		 * Aggregation is periodic, so a window can have negative totals (free-heavy window).
		 * pprof/es tree building uses uint64 values; negative deltas would wrap and corrupt
		 * the profile. Fix by exporting a non-negative retained snapshot, or by splitting
		 * alloc/free into separate positive metrics for pprof/es.
		 */
		Value: value,
	}
}

// Construct pprof (pyroscope data)
func buildPprofData(pctx *context.ProfilerContext, tree []*profiler.TreeItem) (*profiler.ProfileData, error) {
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

func (a *nativeAggregator) reset() {
	a.aggrMap = make(map[aggrKey]*StackEntry)
	a.lockAggrMap = make(map[lockAggrKey]*LockStackEntry)
}
