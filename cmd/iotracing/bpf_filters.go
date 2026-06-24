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

package main

// Constant keys consumed by the iotracing BPF program. Must stay in sync
// with the matching #define names in bpf/iotracing.c — a typo here is a
// silent no-op at attach time.
const (
	bpfFilterEventTimeout = "FILTER_EVENT_TIMEOUT"
	bpfFilterDevIDs       = "FILTER_DEV_IDS"
	bpfFilterDevCount     = "FILTER_DEV_COUNT"
	bpfFilterDevMaxNums   = 16

	// bpfPerfMapName / bpfSourceMapName are map names exposed by the BPF
	// object; the tool dumps the latter and reads events from the former.
	bpfPerfMapName   = "iodelay_perf_events"
	bpfSourceMapName = "io_source_map"
)
