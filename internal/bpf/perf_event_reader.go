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

package bpf

// PerfEventReader reads the eBPF perf_event.
type PerfEventReader interface {
	// ReadInto reads the eBPF perf_event into pdata.
	ReadInto(pdata any) error

	// ReadBatch drains all per-CPU ring buffers currently available within a
	// bounded deadline and returns the parsed events. Each element is a newly
	// allocated value of the same type as pdata.
	ReadBatch(pdata any) ([]any, error)

	// LostSamples returns the cumulative number of samples the kernel reported
	// as dropped because the perf buffer could not keep up.
	LostSamples() uint64

	// Close the PerfEventReader.
	Close() error
}
