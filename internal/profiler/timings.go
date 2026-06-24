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

package profiler

import (
	"sync"
	"time"
)

/*
Main-stages:

	All profilers follow a two-stage pipeline:
	1. Sample Stage: Data collection from target process
	2. Symbolize Stage: Raw data processing and symbol resolution

Sub-stages:

 1. Java-CPU, Python-CPU, Java-Memory:
	[<--------Sample Stage-------->][<--------Symbolize Stage-------->]
    [====run tool====][=serialize==][==byteToTree==][===TreeToPProf===]

 2. Native-CPU, Native-Memory:
    [<-----Sample Stage---->][<-------Symbolize Stage------->]
    [=====bpf load/dump=====][==addrToTree==][==treeToPprof==]

    The timing metrics track duration of each sub-stage for performance analysis.
*/

var (
	SampleSerializeTimeStore  sync.Map
	SymbolizeToPprofTimeStore sync.Map
)

// SetSampleSerializeTimeStamp sets the sample serialize time for a given profiler key
func SetSampleSerializeTimeStamp(profilerName string, t time.Time) {
	SampleSerializeTimeStore.Store(profilerName, t)
}

// SetSymbolizeToPprofTimeStamp sets the symbolize-to-pprof time for a given profiler key
func SetSymbolizeToPprofTimeStamp(profilerName string, t time.Time) {
	SymbolizeToPprofTimeStore.Store(profilerName, t)
}

func GetSampleSerializeTimeStamp(profilerName string) time.Time {
	if v, ok := SampleSerializeTimeStore.Load(profilerName); ok {
		return v.(time.Time)
	}
	return time.Time{}
}

func GetSymbolizeToPprofTimeStamp(profilerName string) time.Time {
	if v, ok := SymbolizeToPprofTimeStore.Load(profilerName); ok {
		return v.(time.Time)
	}
	return time.Time{}
}

type timings struct {
	StartTime          time.Time `json:"start_time"`
	SampleCollectMs    int64     `json:"sample_collect_ms"`
	SampleSerializeMs  int64     `json:"sample_serialize_ms"`
	SymbolizeToTreeMs  int64     `json:"symbolize_to_tree_ms"`
	SymbolizeToPprofMs int64     `json:"symbolize_to_pprof_ms"`
}

func NewTimings(profilerName string, startTime, symbolizeTime, endTime time.Time) *timings {
	serializeStartTime := validTime(GetSampleSerializeTimeStamp(profilerName), symbolizeTime, startTime)
	toPprofStartTime := validTime(GetSymbolizeToPprofTimeStamp(profilerName), endTime, symbolizeTime)

	return &timings{
		StartTime:          startTime,
		SampleCollectMs:    elapsedMillis(startTime, serializeStartTime),
		SampleSerializeMs:  elapsedMillis(serializeStartTime, symbolizeTime),
		SymbolizeToTreeMs:  elapsedMillis(symbolizeTime, toPprofStartTime),
		SymbolizeToPprofMs: elapsedMillis(toPprofStartTime, endTime),
	}
}

// validTime returns the valid time, using fallback if target is zero or before minTime
func validTime(target, fallback, minTime time.Time) time.Time {
	if target.IsZero() || target.Before(minTime) {
		return fallback
	}
	return target
}

// elapsedMillis returns milliseconds between start and end, 0 if end is earlier
func elapsedMillis(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
