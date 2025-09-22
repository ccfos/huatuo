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

import (
	"context"
)

type Option struct {
	KeepaliveTimeout int
}

// The BPF APIs
//
//	The bpf manager has the following APIs:
//
//	// InitBpfManager initializes the bpf manager.
//	InitBpfManager() error
//
//	// CloseBpfManager closes the bpf manager.
//	CloseBpfManager()
//
//	// LoadBpf the bpf and return the bpf.
//	LoadBpf(objName string, consts map[string]any) (BPF, error)

// AttachOption is an option for attaching a program.
type AttachOption struct {
	ProgramName string
	Symbol      string   // symbol for kprobe/kretprobe/tracepoint/raw_tracepoint
	PerfEvent   struct { // BPF_PROG_TYPE_PERF_EVENT
		SamplePeriod, SampleFreq uint64
	}
}

// Info is the info of a bpf.
type Info struct {
	MapsInfo     []MapInfo
	ProgramsInfo []ProgramInfo
}

// MapInfo is the info of a map.
type MapInfo struct {
	ID   uint32
	Name string
}

// ProgramInfo is the info of a program.
type ProgramInfo struct {
	ID          uint32
	Name        string
	SectionName string
}

// MapItem describes a map element with key-value
type MapItem struct {
	Key   []byte
	Value []byte
}

type BPF interface {
	// Name returns the bpf name.
	Name() string

	// MapIDByName gets mapID by Name.
	MapIDByName(name string) uint32

	// ProgIDByName gets progID by Name.
	ProgIDByName(name string) uint32

	// String returns the bpf string.
	String() string

	// Info gets bpf information.
	Info() (*Info, error)

	// Close the bpf bpf.
	Close() error

	// AttachWithOptions attaches programs with options.
	AttachWithOptions(opts []AttachOption) error

	// Attach the default programs.
	Attach() error

	// Detach all programs.
	Detach() error

	// Loaded checks bpf is still loaded.
	Loaded() (bool, error)

	// EventPipe gets event-pipe and returns a PerfEventReader.
	EventPipe(ctx context.Context, mapID, perCPUBuffer uint32) (PerfEventReader, error)

	// EventPipeByName gets event-pipe by the mapName and returns a PerfEventReader.
	EventPipeByName(ctx context.Context, mapName string, perCPUBuffer uint32) (PerfEventReader, error)

	// AttachAndEventPipe attaches and event-pipe and returns a PerfEventReader.
	AttachAndEventPipe(ctx context.Context, mapName string, perCPUBuffer uint32) (PerfEventReader, error)

	// ReadMap read the value content corresponding to a key from a map
	//
	// NOTICE: The content of the key needs to be converted to byte type, and the
	// obtained value is of byte type, which also needs to be converted to the
	// corresponding type.
	ReadMap(mapID uint32, key []byte) ([]byte, error)

	// WriteMapItems writes the value content corresponding to a key to a map.
	WriteMapItems(mapID uint32, items []MapItem) error

	// DeleteMapItems deletes multiple items from a BPF map by keys.
	DeleteMapItems(mapID uint32, keys [][]byte) error

	// DumpMap dump all the context of the map
	DumpMap(mapID uint32) ([]MapItem, error)

	// DumpMapByName dump all the context of the map.
	DumpMapByName(mapName string) ([]MapItem, error)

	// WaitDetachByBreaker check the bpf's status.
	WaitDetachByBreaker(ctx context.Context, cancel context.CancelFunc)
}
