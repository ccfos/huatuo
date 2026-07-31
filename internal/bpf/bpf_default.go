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
	"sync"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"
)

var DefaultObjDir = "bpf"

// Init initializes package-level BPF resources.
func Init(_ *Option) error {
	return unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY,
		Max: unix.RLIM_INFINITY,
	})
}

// Shutdown releases package-level BPF resources.
func Shutdown() {}

type loadedMap struct {
	name   string
	handle *ebpf.Map
}

type loadedProgram struct {
	name          string
	programType   ebpf.ProgramType
	sectionName   string
	sectionPrefix string
	handle        *ebpf.Program
	links         map[string]link.Link
}

// defaultBPF holds loaded BPF maps and programs.
//
// Close waits for in-flight operations before releasing kernel resources.
// Attach and Detach are serialized with map and event operations.
type defaultBPF struct {
	mu               sync.RWMutex
	name             string
	mapsByID         map[uint32]loadedMap
	programsByID     map[uint32]*loadedProgram
	mapIDsByName     map[string]uint32
	programIDsByName map[string]uint32
	perfEvent        *perfEventAttach
	isClosed         bool
}

// _ is a type assertion
var _ BPF = (*defaultBPF)(nil)

// LoadBPFFromBytes loads the BPF object from bytes.
func LoadBPFFromBytes(bpfName string, bpfBytes []byte, consts map[string]any) (BPF, error) {
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	return loadBPFFromReader(bpfName, bytes.NewReader(bpfBytes), consts)
}

// LoadBPFFromCollectionSpec loads the BPF object from a prepared collection spec.
// This allows callers to modify the spec (e.g., inject pcap filters) before loading.
func LoadBPFFromCollectionSpec(bpfName string, spec *ebpf.CollectionSpec, consts map[string]any) (BPF, error) {
	if spec == nil {
		return nil, errors.New("nil collection spec")
	}
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	return loadBPFFromCollectionSpec(bpfName, spec, consts)
}

// LoadBPF loads the BPF object from the default directory and returns it.
func LoadBPF(bpfName string, consts map[string]any) (BPF, error) {
	if err := validateName(bpfName); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(DefaultObjDir, bpfName))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return loadBPFFromReader(bpfName, f, consts)
}

// loadBPFFromReader loads the BPF object from reader.
func loadBPFFromReader(bpfName string, rd io.ReaderAt, consts map[string]any) (BPF, error) {
	specs, err := ebpf.LoadCollectionSpecFromReader(rd)
	if err != nil {
		return nil, fmt.Errorf("parse BPF object %q: %w", bpfName, err)
	}

	return loadBPFFromCollectionSpec(bpfName, specs, consts)
}

func loadBPFFromCollectionSpec(bpfName string, specs *ebpf.CollectionSpec, consts map[string]any) (BPF, error) {
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
		mapsByID:     make(map[uint32]loadedMap),
		programsByID: make(map[uint32]*loadedProgram),
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

		b.mapsByID[uint32(id)] = loadedMap{
			name:   spec.Name,
			handle: cloned,
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

		b.programsByID[uint32(id)] = &loadedProgram{
			name:          spec.Name,
			programType:   spec.Type,
			sectionName:   spec.SectionName,
			sectionPrefix: strings.SplitN(spec.SectionName, "/", 2)[0],
			handle:        cloned,
			links:         make(map[string]link.Link),
		}
	}

	b.mapIDsByName = make(map[string]uint32, len(b.mapsByID))
	for id, m := range b.mapsByID {
		b.mapIDsByName[m.name] = id
	}

	b.programIDsByName = make(map[string]uint32, len(b.programsByID))
	for id, p := range b.programsByID {
		b.programIDsByName[p.name] = id
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
	return b.mapIDsByName[name]
}

func (b *defaultBPF) mapByName(name string) (*ebpf.Map, error) {
	mapID, ok := b.mapIDsByName[name]
	if !ok {
		return nil, fmt.Errorf("%w: name %q", ErrMapNotFound, name)
	}

	return b.mapByID(mapID)
}

func (b *defaultBPF) mapByID(mapID uint32) (*ebpf.Map, error) {
	m, ok := b.mapsByID[mapID]
	if !ok || m.handle == nil {
		return nil, fmt.Errorf("%w: id %d", ErrMapNotFound, mapID)
	}

	return m.handle, nil
}

// ProgramIDByName returns the program ID for name, or zero if it does not exist.
func (b *defaultBPF) ProgramIDByName(name string) uint32 {
	return b.programIDsByName[name]
}

// String returns the bpf string.
func (b *defaultBPF) String() string {
	return fmt.Sprintf("%s#%d#%d", b.name, len(b.mapIDsByName), len(b.programIDsByName))
}

func (b *defaultBPF) acquireReadLock() error {
	b.mu.RLock()
	if b.isClosed {
		b.mu.RUnlock()
		return ErrClosed
	}
	return nil
}

func (b *defaultBPF) acquireWriteLock() error {
	b.mu.Lock()
	if b.isClosed {
		b.mu.Unlock()
		return ErrClosed
	}
	return nil
}

// Info gets defaultBPF information.
func (b *defaultBPF) Info() (*Info, error) {
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	info := &Info{
		MapsInfo:     make([]MapInfo, 0, len(b.mapsByID)),
		ProgramsInfo: make([]ProgramInfo, 0, len(b.programsByID)),
	}

	// maps
	for id, m := range b.mapsByID {
		info.MapsInfo = append(info.MapsInfo, MapInfo{
			ID:   id,
			Name: m.name,
		})
	}

	// programs
	for id, p := range b.programsByID {
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
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.isClosed {
		return nil
	}
	b.isClosed = true

	var closeErrs []error

	for _, p := range b.programsByID {
		for linkKey, l := range p.links {
			if l != nil {
				if err := l.Close(); err != nil {
					closeErrs = append(closeErrs, fmt.Errorf("close link %s in program %s: %w", linkKey, p.name, err))
				}
			}
		}
	}

	for _, p := range b.programsByID {
		if p.handle != nil {
			if err := p.handle.Close(); err != nil {
				closeErrs = append(closeErrs, fmt.Errorf("close program %s: %w", p.name, err))
			}
		}
	}

	for _, m := range b.mapsByID {
		if m.handle != nil {
			if err := m.handle.Close(); err != nil {
				closeErrs = append(closeErrs, fmt.Errorf("close map %s: %w", m.name, err))
			}
		}
	}

	if b.perfEvent != nil {
		if err := b.perfEvent.detach(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("detach perf event: %w", err))
		}
		b.perfEvent = nil
	}

	return errors.Join(closeErrs...)
}

// AttachWithOptions attaches programs with options.
func (b *defaultBPF) AttachWithOptions(opts []AttachOption) error {
	if err := b.acquireWriteLock(); err != nil {
		return err
	}
	defer b.mu.Unlock()

	return b.attachWithOptions(opts)
}

func (b *defaultBPF) attachWithOptions(opts []AttachOption) error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.detach(); detachErr != nil {
				log.Warnf("bpf %s: detach during attach failure also errored: %v", b, detachErr)
			}
		}
	}()

	for _, opt := range opts {
		progID := b.ProgramIDByName(opt.ProgramName)
		program, ok := b.programsByID[progID]
		if !ok {
			return fmt.Errorf("bpf %s: unknown program %q", b, opt.ProgramName)
		}
		isRetprobe := program.programType == ebpf.Kprobe &&
			program.sectionPrefix == "kretprobe"
		if err = validateRetprobeMaxActive(opt.RetprobeMaxActive, isRetprobe); err != nil {
			return fmt.Errorf(
				"bpf %s: program %q: %w",
				b,
				opt.ProgramName,
				err,
			)
		}
		switch program.programType {
		case ebpf.TracePoint:
			// opt.Symbol: <system>/<symbol>
			symbols := strings.SplitN(opt.Symbol, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid symbol: %q", b, opt.Symbol)
			}

			if err = b.attachTracepoint(program, symbols[0], symbols[1]); err != nil {
				return fmt.Errorf("attach tracepoint: %w", err)
			}
		case ebpf.Kprobe:
			// opt.Symbol: <symbol>[+<offset>]
			// opt.Symbol: <symbol>
			if err = b.attachKprobe(
				program,
				opt.Symbol,
				isRetprobe,
				opt.RetprobeMaxActive,
			); err != nil {
				return fmt.Errorf("attach kprobe: %w", err)
			}
		case ebpf.RawTracepoint:
			// opt.Symbol: <symbol>
			if err = b.attachRawTracepoint(program, opt.Symbol); err != nil {
				return fmt.Errorf("attach raw tracepoint: %w", err)
			}
		case ebpf.PerfEvent:
			if err = b.attachPerfEvent(&perfEventOption{
				samplePeriodFreq: opt.PerfEvent.SampleFreq,
				sampleType:       sampleTypeFreq,
				program:          program.handle,
				cpuIDs:           opt.PerfEvent.CPUIDs,
			}); err != nil {
				return fmt.Errorf("attach perf event: %w", err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %q", b, program.programType)
		}
	}

	return nil
}

// Attach the default programs.
func (b *defaultBPF) Attach() error {
	if err := b.acquireWriteLock(); err != nil {
		return err
	}
	defer b.mu.Unlock()

	return b.attach()
}

func (b *defaultBPF) attach() error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.detach(); detachErr != nil {
				log.Warnf("bpf %s: detach during attach failure also errored: %v", b, detachErr)
			}
		}
	}()

	for _, program := range b.programsByID {
		switch program.programType {
		case ebpf.TracePoint:
			// section: tracepoint/<system>/<symbol>
			symbols := strings.SplitN(program.sectionName, "/", 3)
			if len(symbols) != 3 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, program.sectionName)
			}

			if err = b.attachTracepoint(program, symbols[1], symbols[2]); err != nil {
				return fmt.Errorf("attach tracepoint: %w", err)
			}
		case ebpf.Kprobe:
			// section: kprobe/<symbol>[+<offset>]
			// section: kretprobe/<symbol>
			symbols := strings.SplitN(program.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, program.sectionName)
			}

			if err = b.attachKprobe(
				program,
				symbols[1],
				symbols[0] == "kretprobe",
				0,
			); err != nil {
				return fmt.Errorf("attach kprobe: %w", err)
			}
		case ebpf.RawTracepoint:
			// section: raw_tracepoint/<symbol>
			symbols := strings.SplitN(program.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %q", b, program.sectionName)
			}

			if err = b.attachRawTracepoint(program, symbols[1]); err != nil {
				return fmt.Errorf("attach raw tracepoint: %w", err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %q", b, program.programType)
		}
	}

	return nil
}

func validateRetprobeMaxActive(value int, isRetprobe bool) error {
	if value < 0 {
		return fmt.Errorf("retprobe max active must not be negative: %d", value)
	}
	if value != 0 && !isRetprobe {
		return fmt.Errorf(
			"retprobe max active is valid only for kretprobe programs",
		)
	}
	return nil
}

func newKprobeOptions(
	offset uint64,
	retprobeMaxActive int,
	isRetprobe bool,
) (*link.KprobeOptions, error) {
	if err := validateRetprobeMaxActive(retprobeMaxActive, isRetprobe); err != nil {
		return nil, err
	}
	return &link.KprobeOptions{
		Offset:            offset,
		RetprobeMaxActive: retprobeMaxActive,
	}, nil
}

func (b *defaultBPF) attachKprobe(
	program *loadedProgram,
	symbol string,
	isRetprobe bool,
	retprobeMaxActive int,
) error {
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
		if _, ok := program.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
		}

		opts, err := newKprobeOptions(offset, retprobeMaxActive, false)
		if err != nil {
			return err
		}
		l, err := link.Kprobe(symOffsets[0], program.handle, opts)
		if err != nil {
			return fmt.Errorf("attach kprobe %q: %w", symbol, err)
		}

		program.links[linkKey] = l
		log.Debugf("attach kprobe %s, links: %d", symbol, len(program.links))
	} else { // kretprobe
		linkKey := symbol
		if _, ok := program.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
		}

		opts, err := newKprobeOptions(0, retprobeMaxActive, true)
		if err != nil {
			return err
		}
		l, err := link.Kretprobe(symbol, program.handle, opts)
		if err != nil {
			return fmt.Errorf("attach kretprobe %q: %w", symbol, err)
		}

		program.links[linkKey] = l
		log.Debugf("attach kretprobe %s, links: %d", symbol, len(program.links))
	}

	return nil
}

func (b *defaultBPF) attachTracepoint(program *loadedProgram, system, symbol string) error {
	linkKey := fmt.Sprintf("%s/%s", system, symbol)
	if _, ok := program.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
	}

	l, err := link.Tracepoint(system, symbol, program.handle, nil)
	if err != nil {
		return fmt.Errorf("attach tracepoint %s/%s: %w", system, symbol, err)
	}

	program.links[linkKey] = l
	log.Debugf("attach tracepoint %s/%s, links: %d", system, symbol, len(program.links))
	return nil
}

func (b *defaultBPF) attachRawTracepoint(program *loadedProgram, symbol string) error {
	linkKey := symbol
	if _, ok := program.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %q", b, symbol)
	}

	l, err := link.AttachRawTracepoint(link.RawTracepointOptions{
		Name:    symbol,
		Program: program.handle,
	})
	if err != nil {
		return fmt.Errorf("attach raw tracepoint %q: %w", symbol, err)
	}

	program.links[linkKey] = l
	log.Debugf("attach raw tracepoint %s, links: %d", symbol, len(program.links))
	return nil
}

func (b *defaultBPF) attachPerfEvent(opt *perfEventOption) error {
	if b.perfEvent != nil {
		return fmt.Errorf("bpf %s: duplicate perf event attach", b)
	}

	if opt.samplePeriodFreq == 0 {
		return types.ErrArgsInvalid
	}

	event, err := attachPerfEvent(opt)
	if err != nil {
		return fmt.Errorf("attach perf event: %w", err)
	}

	b.perfEvent = event
	log.Debugf("attach perf event, cpuIDs=%v", opt.cpuIDs)
	return nil
}

// Detach all programs. Collects individual detach errors and returns a
// combined error so callers can detect cleanup failures.
func (b *defaultBPF) Detach() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.isClosed {
		return nil
	}

	return b.detach()
}

func (b *defaultBPF) detach() error {
	var detachErrs []error

	for _, program := range b.programsByID {
		for linkKey, l := range program.links {
			if l != nil {
				if err := l.Close(); err != nil {
					detachErrs = append(detachErrs, fmt.Errorf("detach link %s in program %s: %w", linkKey, program.name, err))
					log.Debugf("detach %s in %v: %v", program.sectionName, program.handle, err)
				}
			}
		}
		program.links = make(map[string]link.Link)
	}

	if b.perfEvent != nil {
		if err := b.perfEvent.detach(); err != nil {
			detachErrs = append(detachErrs, fmt.Errorf("detach perf event: %w", err))
		}
		b.perfEvent = nil
	}

	return errors.Join(detachErrs...)
}

// IsLoaded reports whether the BPF object is still loaded.
func (b *defaultBPF) IsLoaded() (bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return !b.isClosed, nil
}

// EventPipe gets event-pipe and returns a PerfEventReader.
func (b *defaultBPF) EventPipe(ctx context.Context, mapID, perCPUBufSize uint32) (PerfEventReader, error) {
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByID(mapID)
	if err != nil {
		return nil, err
	}

	reader, err := newPerfEventReader(ctx, m, int(perCPUBufSize))
	if err != nil {
		return nil, err
	}

	log.Debugf("event-pipe %d, perCPUBufSize %d", mapID, perCPUBufSize)
	return reader, nil
}

// EventPipeByName gets event-pipe by the mapName and returns a PerfEventReader.
func (b *defaultBPF) EventPipeByName(ctx context.Context, mapName string, perCPUBufSize uint32) (PerfEventReader, error) {
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByName(mapName)
	if err != nil {
		return nil, err
	}

	reader, err := newPerfEventReader(ctx, m, int(perCPUBufSize))
	if err != nil {
		return nil, err
	}

	log.Debugf("event-pipe %s, perCPUBufSize %d", mapName, perCPUBufSize)
	return reader, nil
}

// AttachAndEventPipe attaches and event-pipe and returns a PerfEventReader.
func (b *defaultBPF) AttachAndEventPipe(ctx context.Context, mapName string, perCPUBufSize uint32) (PerfEventReader, error) {
	if err := b.acquireWriteLock(); err != nil {
		return nil, err
	}
	defer b.mu.Unlock()

	m, err := b.mapByName(mapName)
	if err != nil {
		return nil, err
	}

	reader, err := newPerfEventReader(ctx, m, int(perCPUBufSize))
	if err != nil {
		return nil, err
	}

	if err := b.attach(); err != nil {
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
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByID(mapID)
	if err != nil {
		return nil, err
	}

	val, err := m.LookupBytes(key)
	if err != nil {
		return nil, err
	}

	return val, nil
}

// WriteMapItems write the value content corresponding to a key to a map.
func (b *defaultBPF) WriteMapItems(mapID uint32, items []MapItem) error {
	if err := b.acquireReadLock(); err != nil {
		return err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByID(mapID)
	if err != nil {
		return err
	}

	for _, item := range items {
		if err := m.Update(item.Key, item.Value, ebpf.UpdateAny); err != nil {
			return fmt.Errorf("map %d, key %v: update: %w", mapID, item.Key, err)
		}
	}
	return nil
}

// DeleteMapItems deletes multiple items from a BPF map by keys.
func (b *defaultBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	if err := b.acquireReadLock(); err != nil {
		return err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByID(mapID)
	if err != nil {
		return err
	}

	for _, k := range keys {
		if err := m.Delete(k); err != nil {
			return fmt.Errorf("map %d, key %v: delete: %w", mapID, k, err)
		}
	}
	return nil
}

// DumpMap dump all the context of the map
func (b *defaultBPF) DumpMap(mapID uint32) ([]MapItem, error) {
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByID(mapID)
	if err != nil {
		return nil, err
	}

	items, err := b.dumpMap(m)
	if err != nil {
		return nil, fmt.Errorf("map %d: %w", mapID, err)
	}

	return items, nil
}

func (b *defaultBPF) dumpMap(m *ebpf.Map) ([]MapItem, error) {
	switch m.Type() {
	case ebpf.PerCPUHash, ebpf.PerCPUArray, ebpf.LRUCPUHash, ebpf.PerCPUCGroupStorage:
		return dumpPerCPUMap(m)
	}

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
		return nil, fmt.Errorf("iterate: %w", err)
	}

	return items, nil
}

func dumpPerCPUMap(m *ebpf.Map) ([]MapItem, error) {
	var (
		items       []MapItem
		previousKey any
		maxEntries  = m.MaxEntries()
	)

	for count := uint32(0); ; count++ {
		key := make([]byte, m.KeySize())

		err := m.NextKey(previousKey, key)
		if errors.Is(err, ebpf.ErrKeyNotExist) {
			// No next key means the map was fully traversed.
			return items, nil
		}

		if err != nil {
			return nil, fmt.Errorf("iterate: get next key: %w", err)
		}

		// A concurrent deletion can make NextKey restart from the first key.
		if count == maxEntries {
			return nil, fmt.Errorf("iterate: %w", ebpf.ErrIterationAborted)
		}

		value, err := m.LookupBytes(key)
		if err != nil {
			return nil, fmt.Errorf("iterate: look up next key: %w", err)
		}
		if value != nil {
			items = append(items, MapItem{
				Key:   key,
				Value: value,
			})
		}
		previousKey = key
	}
}

// DumpMapByName dump all the context of the map.
func (b *defaultBPF) DumpMapByName(mapName string) ([]MapItem, error) {
	if err := b.acquireReadLock(); err != nil {
		return nil, err
	}
	defer b.mu.RUnlock()

	m, err := b.mapByName(mapName)
	if err != nil {
		return nil, err
	}

	items, err := b.dumpMap(m)
	if err != nil {
		return nil, fmt.Errorf("map %q: %w", mapName, err)
	}

	return items, nil
}

// DetachOnContextDone is a hook for context-driven detach handling.
func (b *defaultBPF) DetachOnContextDone(ctx context.Context, cancel context.CancelFunc) {
	// TODO: implement
}
