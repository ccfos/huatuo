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

//go:build !didi

package bpf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"
)

var DefaultObjDir = "bpf"

// NewManager initializes the bpf manager.
func NewManager(opt *Option) error {
	return unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY,
		Max: unix.RLIM_INFINITY,
	})
}

// Close closes the bpf manager.
func Close() {}

type mapSpec struct {
	name   string
	cloned *ebpf.Map
}

type programSpec struct {
	name          string
	specType      ebpf.ProgramType
	sectionName   string
	sectionPrefix string
	cloned        *ebpf.Program
	links         map[string]link.Link
}

// defaultBPF holds loaded BPF maps and programs.
//
// NOTE: defaultBPF is NOT thread-safe. Concurrent calls to Attach/Detach/Close
// or map operations will race. Callers must serialize lifecycle methods.
// runtime.SetFinalizer may invoke Close from any goroutine once the object
// becomes unreachable, so callers must retain a reference until explicitly
// closed.
type defaultBPF struct {
	name            string
	mapSpecs        map[uint32]mapSpec
	programSpecs    map[uint32]programSpec
	mapName2IDs     map[string]uint32
	programName2IDs map[string]uint32
	innerPerfEvent  *perfEventAttach
	closed          atomic.Bool
}

// _ is a type assertion
var _ BPF = (*defaultBPF)(nil)

// LoadBpfFromBytes loads the bpf from bytes.
func LoadBpfFromBytes(bpfName string, bpfBytes []byte, consts map[string]any) (BPF, error) {
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	return loadBpfFromReader(bpfName, bytes.NewReader(bpfBytes), consts)
}

// LoadBpfFromCollectionSpec loads the bpf from a prepared collection spec.
// This allows callers to modify the spec (e.g., inject pcap filters) before loading.
func LoadBpfFromCollectionSpec(bpfName string, spec *ebpf.CollectionSpec, consts map[string]any) (BPF, error) {
	if spec == nil {
		return nil, errors.New("nil collection spec")
	}
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	return loadBpfFromCollectionSpec(bpfName, spec, consts)
}

// LoadBpf loads the BPF object from the default directory and returns it.
func LoadBpf(bpfName string, consts map[string]any) (BPF, error) {
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(DefaultObjDir, bpfName))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return loadBpfFromReader(bpfName, f, consts)
}

// loadBpfFromReader loads the bpf from reader.
func loadBpfFromReader(bpfName string, rd io.ReaderAt, consts map[string]any) (BPF, error) {
	specs, err := ebpf.LoadCollectionSpecFromReader(rd)
	if err != nil {
		return nil, fmt.Errorf("parse BPF object %q: %w", bpfName, err)
	}

	return loadBpfFromCollectionSpec(bpfName, specs, consts)
}

func loadBpfFromCollectionSpec(bpfName string, specs *ebpf.CollectionSpec, consts map[string]any) (BPF, error) {
	// RewriteConstants
	if consts != nil {
		if err := specs.RewriteConstants(consts); err != nil {
			return nil, fmt.Errorf("rewrite constants: %w", err)
		}
	}

	// loads Maps and Programs into the kernel.
	coll, err := ebpf.NewCollection(specs)
	if err != nil {
		return nil, fmt.Errorf("create BPF collection: %w", err)
	}
	defer coll.Close()

	b := &defaultBPF{
		name:         bpfName,
		mapSpecs:     make(map[uint32]mapSpec),
		programSpecs: make(map[uint32]programSpec),
	}

	// maps
	for name, spec := range specs.Maps {
		m, ok := coll.Maps[name]
		if !ok {
			continue
		}

		info, err := m.Info()
		if err != nil {
			return nil, fmt.Errorf("get map info: %w", err)
		}

		id, ok := info.ID()
		if !ok {
			return nil, fmt.Errorf("invalid map ID: %d", id)
		}

		cloned, err := m.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone map: %w", err)
		}

		b.mapSpecs[uint32(id)] = mapSpec{
			name:   spec.Name,
			cloned: cloned,
		}
	}

	// programs
	for name, spec := range specs.Programs {
		p, ok := coll.Programs[name]
		if !ok {
			continue
		}

		info, err := p.Info()
		if err != nil {
			return nil, fmt.Errorf("get program info: %w", err)
		}

		id, ok := info.ID()
		if !ok {
			return nil, fmt.Errorf("invalid program ID: %d", id)
		}

		cloned, err := p.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone program: %w", err)
		}

		b.programSpecs[uint32(id)] = programSpec{
			name:          spec.Name,
			specType:      spec.Type,
			sectionName:   spec.SectionName,
			sectionPrefix: strings.SplitN(spec.SectionName, "/", 2)[0],
			cloned:        cloned,
			links:         make(map[string]link.Link),
		}
	}

	// mapName2IDs
	b.mapName2IDs = make(map[string]uint32, len(b.mapSpecs))
	for id, m := range b.mapSpecs {
		b.mapName2IDs[m.name] = id
	}

	// programName2IDs
	b.programName2IDs = make(map[string]uint32, len(b.programSpecs))
	for id, p := range b.programSpecs {
		b.programName2IDs[p.name] = id
	}

	log.Debugf("loaded bpf: %s", b)

	// auto clean
	runtime.SetFinalizer(b, (*defaultBPF).Close)
	return b, nil
}

// Name returns the name of the bpf.
func (b *defaultBPF) Name() string {
	return b.name
}

// MapIDByName gets mapID by Name. Returns 0 if the name does not exist.
func (b *defaultBPF) MapIDByName(name string) uint32 {
	return b.mapName2IDs[name]
}

// ProgIDByName gets progID by Name. Returns 0 if the name does not exist.
func (b *defaultBPF) ProgIDByName(name string) uint32 {
	return b.programName2IDs[name]
}

// String returns the bpf string.
func (b *defaultBPF) String() string {
	return fmt.Sprintf("%s#%d#%d", b.name, len(b.mapSpecs), len(b.programSpecs))
}

// Info gets defaultBPF information.
func (b *defaultBPF) Info() (*Info, error) {
	info := &Info{
		MapsInfo:     make([]MapInfo, 0, len(b.mapSpecs)),
		ProgramsInfo: make([]ProgramInfo, 0, len(b.programSpecs)),
	}

	// maps
	for id, m := range b.mapSpecs {
		info.MapsInfo = append(info.MapsInfo, MapInfo{
			ID:   id,
			Name: m.name,
		})
	}

	// programs
	for id, p := range b.programSpecs {
		info.ProgramsInfo = append(info.ProgramsInfo, ProgramInfo{
			ID:          id,
			Name:        p.name,
			SectionName: p.sectionName,
		})
	}

	return info, nil
}

// Close the bpf. Collects individual close errors and returns a combined error
// so callers can detect cleanup failures.
func (b *defaultBPF) Close() error {
	if b.closed.Swap(true) {
		return nil
	}

	var closeErrs []error

	for _, p := range b.programSpecs {
		for linkKey, l := range p.links {
			if l != nil {
				if err := l.Close(); err != nil {
					closeErrs = append(closeErrs, fmt.Errorf("close link %s in program %s: %w", linkKey, p.name, err))
				}
			}
		}
	}

	for _, p := range b.programSpecs {
		if p.cloned != nil {
			if err := p.cloned.Close(); err != nil {
				closeErrs = append(closeErrs, fmt.Errorf("close program %s: %w", p.name, err))
			}
		}
	}

	for _, m := range b.mapSpecs {
		if m.cloned != nil {
			if err := m.cloned.Close(); err != nil {
				closeErrs = append(closeErrs, fmt.Errorf("close map %s: %w", m.name, err))
			}
		}
	}

	if b.innerPerfEvent != nil {
		if err := b.innerPerfEvent.detach(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("detach perf event: %w", err))
		}
		b.innerPerfEvent = nil
	}

	return errors.Join(closeErrs...)
}

// AttachWithOptions attaches programs with options.
func (b *defaultBPF) AttachWithOptions(opts []AttachOption) error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.Detach(); detachErr != nil {
				log.Warnf("bpf %s: detach during attach failure also errored: %v", b, detachErr)
			}
		}
	}()

	for _, opt := range opts {
		progID := b.ProgIDByName(opt.ProgramName)
		spec, ok := b.programSpecs[progID]
		if !ok {
			return fmt.Errorf("bpf %s: unknown program %q", b, opt.ProgramName)
		}
		switch spec.specType {
		case ebpf.TracePoint:
			// opt.Symbol: <system>/<symbol>
			symbols := strings.SplitN(opt.Symbol, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid symbol: %q", b, opt.Symbol)
			}

			if err = b.attachTracepoint(progID, symbols[0], symbols[1]); err != nil {
				return fmt.Errorf("attach tracepoint: %w", err)
			}
		case ebpf.Kprobe:
			// opt.Symbol: <symbol>[+<offset>]
			// opt.Symbol: <symbol>
			if err = b.attachKprobe(progID, opt.Symbol, spec.sectionPrefix == "kretprobe"); err != nil {
				return fmt.Errorf("attach kprobe: %w", err)
			}
		case ebpf.RawTracepoint:
			// opt.Symbol: <symbol>
			if err = b.attachRawTracepoint(progID, opt.Symbol); err != nil {
				return fmt.Errorf("attach raw tracepoint: %w", err)
			}
		case ebpf.PerfEvent:
			if err = b.attachPerfEvent(&perfEventOption{
				samplePeriodFreq: opt.PerfEvent.SampleFreq,
				sampleType:       sampleTypeFreq,
				program:          spec.cloned,
				cpuIDs:           opt.PerfEvent.CPUIDs,
			}); err != nil {
				return fmt.Errorf("attach perf event: %w", err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %q", b, spec.specType)
		}
	}

	return nil
}

// Attach the default programs.
func (b *defaultBPF) Attach() error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.Detach(); detachErr != nil {
				log.Warnf("bpf %s: detach during attach failure also errored: %v", b, detachErr)
			}
		}
	}()

	for progID, spec := range b.programSpecs {
		switch spec.specType {
		case ebpf.TracePoint:
			// section: tracepoint/<system>/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 3)
			if len(symbols) != 3 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, spec.sectionName)
			}

			if err = b.attachTracepoint(progID, symbols[1], symbols[2]); err != nil {
				return fmt.Errorf("attach tracepoint: %w", err)
			}
		case ebpf.Kprobe:
			// section: kprobe/<symbol>[+<offset>]
			// section: kretprobe/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, spec.sectionName)
			}

			if err = b.attachKprobe(progID, symbols[1], symbols[0] == "kretprobe"); err != nil {
				return fmt.Errorf("attach kprobe: %w", err)
			}
		case ebpf.RawTracepoint:
			// section: raw_tracepoint/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, spec.sectionName)
			}

			if err = b.attachRawTracepoint(progID, symbols[1]); err != nil {
				return fmt.Errorf("attach raw tracepoint: %w", err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %q", b, spec.specType)
		}
	}

	return nil
}

func (b *defaultBPF) attachKprobe(progID uint32, symbol string, isRetprobe bool) error {
	spec := b.programSpecs[progID]

	if !isRetprobe { // kprobe
		// : <symbol>[+<offset>]
		// : <symbol>
		var (
			err    error
			offset uint64
		)

		symOffsets := strings.Split(symbol, "+")
		if len(symOffsets) > 2 {
			return fmt.Errorf("bpf %s: invalid symbol: %q", b, symbol)
		} else if len(symOffsets) == 2 {
			offset, err = strconv.ParseUint(symOffsets[1], 10, 64)
			if err != nil {
				return fmt.Errorf("bpf %s: invalid symbol: %q", b, symbol)
			}
		}

		linkKey := fmt.Sprintf("%s+%d", symOffsets[0], offset)
		if _, ok := spec.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
		}

		opts := link.KprobeOptions{
			Offset: offset,
		}
		l, err := link.Kprobe(symOffsets[0], spec.cloned, &opts)
		if err != nil {
			return fmt.Errorf("attach kprobe %q: %w", symbol, err)
		}

		spec.links[linkKey] = l
		log.Debugf("attach kprobe %s, links: %d", symbol, len(spec.links))
	} else { // kretprobe
		linkKey := symbol
		if _, ok := spec.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
		}

		l, err := link.Kretprobe(symbol, spec.cloned, nil)
		if err != nil {
			return fmt.Errorf("attach kretprobe %q: %w", symbol, err)
		}

		spec.links[linkKey] = l
		log.Debugf("attach kretprobe %s, links: %d", symbol, len(spec.links))
	}

	return nil
}

func (b *defaultBPF) attachTracepoint(progID uint32, system, symbol string) error {
	spec := b.programSpecs[progID]

	linkKey := fmt.Sprintf("%s/%s", system, symbol)
	if _, ok := spec.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
	}

	l, err := link.Tracepoint(system, symbol, spec.cloned, nil)
	if err != nil {
		return fmt.Errorf("attach tracepoint %s/%s: %w", system, symbol, err)
	}

	spec.links[linkKey] = l
	log.Debugf("attach tracepoint %s/%s, links: %d", system, symbol, len(spec.links))
	return nil
}

func (b *defaultBPF) attachRawTracepoint(progID uint32, symbol string) error {
	spec := b.programSpecs[progID]

	linkKey := symbol
	if _, ok := spec.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
	}

	l, err := link.AttachRawTracepoint(link.RawTracepointOptions{
		Name:    symbol,
		Program: spec.cloned,
	})
	if err != nil {
		return fmt.Errorf("attach raw tracepoint %q: %w", symbol, err)
	}

	spec.links[linkKey] = l
	log.Debugf("attach raw tracepoint %s, links: %d", symbol, len(spec.links))
	return nil
}

func (b *defaultBPF) attachPerfEvent(opt *perfEventOption) error {
	if b.innerPerfEvent != nil {
		return fmt.Errorf("bpf %s: duplicate perf event attach", b)
	}

	if opt.samplePeriodFreq == 0 {
		return types.ErrArgsInvalid
	}

	event, err := attachPerfEvent(opt)
	if err != nil {
		return fmt.Errorf("attach perf event: %w", err)
	}

	b.innerPerfEvent = event
	log.Debugf("attach perf event, cpuIDs=%v", opt.cpuIDs)
	return nil
}

// Detach all programs. Collects individual detach errors and returns a
// combined error so callers can detect cleanup failures.
func (b *defaultBPF) Detach() error {
	if b.closed.Load() {
		return nil
	}

	var detachErrs []error

	for id, spec := range b.programSpecs {
		for linkKey, l := range spec.links {
			if l != nil {
				if err := l.Close(); err != nil {
					detachErrs = append(detachErrs, fmt.Errorf("detach link %s in program %s: %w", linkKey, spec.name, err))
					log.Debugf("detach %s in %v: %v", spec.sectionName, spec.cloned, err)
				}
			}
		}
		spec.links = make(map[string]link.Link)
		b.programSpecs[id] = spec
	}

	if b.innerPerfEvent != nil {
		if err := b.innerPerfEvent.detach(); err != nil {
			detachErrs = append(detachErrs, fmt.Errorf("detach perf event: %w", err))
		}
		b.innerPerfEvent = nil
	}

	return errors.Join(detachErrs...)
}

// Loaded checks bpf is still loaded.
func (b *defaultBPF) Loaded() (bool, error) {
	return true, nil
}

// EventPipe gets event-pipe and returns a PerfEventReader.
func (b *defaultBPF) EventPipe(ctx context.Context, mapID, perCPUBufSize uint32) (PerfEventReader, error) {
	reader, err := newPerfEventReader(ctx, b.mapSpecs[mapID].cloned, int(perCPUBufSize))
	if err != nil {
		return nil, err
	}

	log.Debugf("event-pipe %d, perCPUBufSize %d", mapID, perCPUBufSize)
	return reader, nil
}

// EventPipeByName gets event-pipe by the mapName and returns a PerfEventReader.
func (b *defaultBPF) EventPipeByName(ctx context.Context, mapName string, perCPUBufSize uint32) (PerfEventReader, error) {
	return b.EventPipe(ctx, b.MapIDByName(mapName), perCPUBufSize)
}

// AttachAndEventPipe attaches and event-pipe and returns a PerfEventReader.
func (b *defaultBPF) AttachAndEventPipe(ctx context.Context, mapName string, perCPUBufSize uint32) (PerfEventReader, error) {
	reader, err := b.EventPipeByName(ctx, mapName, perCPUBufSize)
	if err != nil {
		return nil, err
	}

	if err := b.Attach(); err != nil {
		return nil, errors.Join(err, reader.Close())
	}

	log.Debugf("attach and event-pipe %s, perCPUBufSize %d", mapName, perCPUBufSize)
	return reader, nil
}

// ReadMap read the value content corresponding to a key from a map
//
// NOTICE: The content of the key needs to be converted to byte type, and the
// obtained value is of byte type, which also needs to be converted to the
// corresponding type.
func (b *defaultBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	val, err := b.mapSpecs[mapID].cloned.LookupBytes(key)
	if err != nil {
		return nil, err
	}

	return val, nil
}

// WriteMapItems write the value content corresponding to a key to a map.
func (b *defaultBPF) WriteMapItems(mapID uint32, items []MapItem) error {
	m := b.mapSpecs[mapID].cloned

	for _, item := range items {
		if err := m.Update(item.Key, item.Value, ebpf.UpdateAny); err != nil {
			return fmt.Errorf("map %d, key %v: update: %w", mapID, item.Key, err)
		}
	}
	return nil
}

// DeleteMapItems deletes multiple items from a BPF map by keys.
func (b *defaultBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	m := b.mapSpecs[mapID].cloned

	for _, k := range keys {
		if err := m.Delete(k); err != nil {
			return fmt.Errorf("map %d, key %v: delete: %w", mapID, k, err)
		}
	}
	return nil
}

// DumpMap dump all the context of the map
func (b *defaultBPF) DumpMap(mapID uint32) ([]MapItem, error) {
	m := b.mapSpecs[mapID].cloned

	var items []MapItem
	key := make([]byte, m.KeySize())
	val := make([]byte, m.ValueSize())
	iter := m.Iterate()
	for iter.Next(&key, &val) {
		items = append(items, MapItem{
			Key:   append([]byte(nil), key...),
			Value: append([]byte(nil), val...),
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("map %d: iterate: %w", mapID, err)
	}

	return items, nil
}

// DumpMapByName dump all the context of the map.
func (b *defaultBPF) DumpMapByName(mapName string) ([]MapItem, error) {
	return b.DumpMap(b.MapIDByName(mapName))
}

// WaitDetachByBreaker check the bpf's status.
func (b *defaultBPF) WaitDetachByBreaker(ctx context.Context, cancel context.CancelFunc) {
	// TODO: implement
}
