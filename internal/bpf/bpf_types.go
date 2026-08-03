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

package bpf

import (
	"errors"
)

var (
	// ErrClosed is returned when an operation uses a closed BPF object.
	ErrClosed = errors.New("bpf: object is closed")
	// ErrMapNotFound indicates that a requested BPF map is unavailable.
	ErrMapNotFound = errors.New("bpf: map not found")
	// ErrDuplicateAttach indicates that a BPF attach target is already in use.
	ErrDuplicateAttach = errors.New("bpf: duplicate attach")
)

type Option struct {
	KeepaliveTimeout int
}

// AttachOption is an option for attaching a program.
type AttachOption struct {
	ProgramName string
	Symbol      string // symbol for kprobe/kretprobe/tracepoint/raw_tracepoint

	Kprobe struct {
		// RetprobeMaxActive limits concurrent kretprobe instances.
		// A non-zero value forces cilium/ebpf to use tracefs.
		RetprobeMaxActive int
	}

	PerfEvent struct { // BPF_PROG_TYPE_PERF_EVENT
		SamplePeriod, SampleFreq uint64
		CPUIDs                   []int
	}
}

// Info holds loaded BPF object metadata.
type Info struct {
	MapsInfo     []MapInfo
	ProgramsInfo []ProgramInfo
}

// MapInfo identifies a loaded BPF map.
type MapInfo struct {
	ID   uint32
	Name string
}

// ProgramInfo identifies a loaded BPF program.
type ProgramInfo struct {
	ID          uint32
	Name        string
	SectionName string
}

// MapItem describes a map element with key-value.
type MapItem struct {
	Key   []byte
	Value []byte
}
