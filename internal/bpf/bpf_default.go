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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"
)

// InitBpfManager initializes the bpf manager.
func InitBpfManager(opt *Option) error {
	return unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY,
		Max: unix.RLIM_INFINITY,
	})
}

// CloseBpfManager closes the bpf manager.
func CloseBpfManager() {}

type mapSpec struct {
	name string
	bMap *ebpf.Map
}

type programSpec struct {
	name          string
	specType      ebpf.ProgramType
	sectionName   string
	sectionPrefix string
	bProg         *ebpf.Program
	links         map[string]link.Link
}

type defaultBPF struct {
	name            string
	mapSpecs        map[uint32]mapSpec
	programSpecs    map[uint32]programSpec
	mapName2IDs     map[string]uint32
	programName2IDs map[string]uint32
	innerPerfEvent  *perfEventPMU
}

// _ is a type assertion
var _ BPF = (*defaultBPF)(nil)

// LoadBpfFromBytes loads the bpf from bytes.
func LoadBpfFromBytes(bpfName string, bpfBytes []byte, consts map[string]any) (BPF, error) {
	return loadBpfFromReader(bpfName, bytes.NewReader(bpfBytes), consts)
}

// LoadBpf the bpf and return the bpf.
func LoadBpf(bpfName string, consts map[string]any) (BPF, error) {
	f, err := os.Open(filepath.Join(conf.CoreBinDir, "../bpf", bpfName))
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
		return nil, fmt.Errorf("can't parse the bpf file %s: %w", bpfName, err)
	}

	// RewriteConstants
	if consts != nil {
		if err := specs.RewriteConstants(consts); err != nil {
			return nil, fmt.Errorf("can't rewrite constants: %w", err)
		}
	}

	// loads Maps and Programs into the kernel.
	coll, err := ebpf.NewCollection(specs)
	if err != nil {
		return nil, fmt.Errorf("can't new the bpf collection: %w", err)
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
			return nil, fmt.Errorf("can't get map info: %w", err)
		}

		id, ok := info.ID()
		if !ok {
			return nil, fmt.Errorf("invalid map ID: %v", id)
		}

		bMap, err := m.Clone()
		if err != nil {
			return nil, fmt.Errorf("can't clone map: %w", err)
		}

		b.mapSpecs[uint32(id)] = mapSpec{
			name: spec.Name,
			bMap: bMap,
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
			return nil, fmt.Errorf("can't get program info: %w", err)
		}

		id, ok := info.ID()
		if !ok {
			return nil, fmt.Errorf("invalid program ID: %v", id)
		}

		bProg, err := p.Clone()
		if err != nil {
			return nil, fmt.Errorf("can't clone program: %w", err)
		}

		b.programSpecs[uint32(id)] = programSpec{
			name:          spec.Name,
			specType:      spec.Type,
			sectionName:   spec.SectionName,
			sectionPrefix: strings.SplitN(spec.SectionName, "/", 2)[0],
			bProg:         bProg,
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

	log.Infof("loaded bpf: %s", b)

	// auto clean
	runtime.SetFinalizer(b, (*defaultBPF).Close)
	return b, nil
}

// Name returns the name of the bpf.
func (b *defaultBPF) Name() string {
	return b.name
}

// MapIDByName gets mapID by Name.
func (b *defaultBPF) MapIDByName(name string) uint32 {
	return b.mapName2IDs[name]
}

// ProgIDByName gets progID by Name.
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

// Close the bpf.
func (b *defaultBPF) Close() error {
	for _, m := range b.mapSpecs {
		m.bMap.Close()
	}

	for _, p := range b.programSpecs {
		for _, l := range p.links {
			l.Close()
		}
		p.bProg.Close()
	}

	return nil
}

// AttachWithOptions attaches programs with options.
func (b *defaultBPF) AttachWithOptions(opts []AttachOption) error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			_ = b.Detach()
		}
	}()

	for _, opt := range opts {
		progID := b.ProgIDByName(opt.ProgramName)
		spec := b.programSpecs[progID]
		switch spec.specType {
		case ebpf.TracePoint:
			// opt.Symbol: <system>/<symbol>
			symbols := strings.SplitN(opt.Symbol, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid symbol: %s", b, opt.Symbol)
			}

			if err = b.attachTracepoint(progID, symbols[0], symbols[1]); err != nil {
				return fmt.Errorf("attach tracepoint with options %v: %w", opt, err)
			}
		case ebpf.Kprobe:
			// opt.Symbol: <symbol>[+<offset>]
			// opt.Symbol: <symbol>
			if err = b.attachKprobe(progID, opt.Symbol, spec.sectionPrefix == "kretprobe"); err != nil {
				return fmt.Errorf("attach kprobe with options %v: %w", opt, err)
			}
		case ebpf.RawTracepoint:
			// opt.Symbol: <symbol>
			if err = b.attachRawTracepoint(progID, opt.Symbol); err != nil {
				return fmt.Errorf("attach raw tracepoint with options %v: %w", opt, err)
			}
		case ebpf.PerfEvent:
			if err = b.attachPerfEvent(progID, opt.PerfEvent.SamplePeriod, opt.PerfEvent.SampleFreq); err != nil {
				return fmt.Errorf("attach perf event with options %v: %w", opt, err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %s", b, spec.specType)
		}
	}

	return nil
}

// Attach the default programs.
func (b *defaultBPF) Attach() error {
	var err error

	defer func() {
		if err != nil { // detach all programs when error.
			_ = b.Detach()
		}
	}()

	for progID, spec := range b.programSpecs {
		switch spec.specType {
		case ebpf.TracePoint:
			// section: tracepoint/<system>/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 3)
			if len(symbols) != 3 {
				return fmt.Errorf("bpf %s: invalid section name: %s", b, spec.sectionName)
			}

			if err = b.attachTracepoint(progID, symbols[1], symbols[2]); err != nil {
				return fmt.Errorf("attach tracepoint: %w", err)
			}
		case ebpf.Kprobe:
			// section: kprobe/<symbol>[+<offset>]
			// section: kretprobe/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %s", b, spec.sectionName)
			}

			if err = b.attachKprobe(progID, symbols[1], symbols[0] == "kretprobe"); err != nil {
				return fmt.Errorf("attach kprobe: %w", err)
			}
		case ebpf.RawTracepoint:
			// section: raw_tracepoint/<symbol>
			symbols := strings.SplitN(spec.sectionName, "/", 2)
			if len(symbols) != 2 {
				return fmt.Errorf("bpf %s: invalid section name: %s", b, spec.sectionName)
			}

			if err = b.attachRawTracepoint(progID, symbols[1]); err != nil {
				return fmt.Errorf("attach raw tracepoint: %w", err)
			}
		default:
			return fmt.Errorf("bpf %s: unsupported program type: %s", b, spec.specType)
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
			return fmt.Errorf("bpf %s: invalid symbol: %s", b, symbol)
		} else if len(symOffsets) == 2 {
			offset, err = strconv.ParseUint(symOffsets[1], 10, 64)
			if err != nil {
				return fmt.Errorf("bpf %s: invalid symbol: %s", b, symbol)
			}
		}

		linkKey := fmt.Sprintf("%s+%d", symOffsets[0], offset)
		if _, ok := spec.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %s", b, symbol)
		}

		opts := link.KprobeOptions{
			Offset: offset,
		}
		l, err := link.Kprobe(symOffsets[0], spec.bProg, &opts)
		if err != nil {
			return fmt.Errorf("can't attach kprobe %s in %v: %w", symbol, spec.bProg, err)
		}

		spec.links[linkKey] = l
		log.Infof("attach kprobe %s in %v, links: %v", symbol, spec.bProg, spec.links)
	} else { // kretprobe
		linkKey := symbol
		if _, ok := spec.links[linkKey]; ok {
			return fmt.Errorf("bpf %s: duplicate symbol: %s", b, symbol)
		}

		l, err := link.Kretprobe(symbol, spec.bProg, nil)
		if err != nil {
			return fmt.Errorf("can't attach kretprobe %s in %v: %w", symbol, spec.bProg, err)
		}

		spec.links[linkKey] = l
		log.Infof("attach kretprobe %s in %v, links: %v", symbol, spec.bProg, spec.links)
	}

	return nil
}

func (b *defaultBPF) attachTracepoint(progID uint32, system, symbol string) error {
	spec := b.programSpecs[progID]

	linkKey := fmt.Sprintf("%s/%s", system, symbol)
	if _, ok := spec.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %s", b, symbol)
	}

	l, err := link.Tracepoint(system, symbol, spec.bProg, nil)
	if err != nil {
		return fmt.Errorf("can't attach tracepoint %s/%s in %v: %w", system, symbol, spec.bProg, err)
	}

	spec.links[linkKey] = l
	log.Infof("attach tracepoint %s/%s in %v, links: %v", system, symbol, spec.bProg, spec.links)
	return nil
}

func (b *defaultBPF) attachRawTracepoint(progID uint32, symbol string) error {
	spec := b.programSpecs[progID]

	linkKey := symbol
	if _, ok := spec.links[linkKey]; ok {
		return fmt.Errorf("bpf %s: duplicate symbol: %s", b, symbol)
	}

	l, err := link.AttachRawTracepoint(link.RawTracepointOptions{
		Name:    symbol,
		Program: spec.bProg,
	})
	if err != nil {
		return fmt.Errorf("can't attach raw tracepoint %s in %v: %w", symbol, spec.bProg, err)
	}

	spec.links[linkKey] = l
	log.Infof("attach raw tracepoint %s in %v, links: %v", symbol, spec.bProg, spec.links)
	return nil
}

func (b *defaultBPF) attachPerfEvent(progID uint32, samplePeriod, sampleFreq uint64) error {
	if b.innerPerfEvent != nil {
		return fmt.Errorf("bpf %s duplicated symbol: %s", b, perfEventPmuSysbmol)
	}

	if samplePeriod != 0 {
		return types.ErrNotSupported
	}

	if sampleFreq == 0 {
		return types.ErrArgsInvalid
	}

	spec := b.programSpecs[progID]
	event, err := attachPerfEventPMU(&perfEventPMUOption{
		samplePeriodFreq: sampleFreq,
		sampleType:       sampleTypeFreq,
		program:          spec.bProg,
	})
	if err != nil {
		return fmt.Errorf("attach bpf perfevent PERF_COUNT_SW_CPU_CLOCK: %w", err)
	}

	b.innerPerfEvent = event
	log.Debugf("attach bpf perfevent: %v", spec.bProg)
	return nil
}

// Detach all programs.
func (b *defaultBPF) Detach() error {
	for _, spec := range b.programSpecs {
		for _, l := range spec.links {
			err := l.Close()
			log.Infof("detach %s in %v: %v", spec.sectionName, spec.bProg, err)
		}
	}

	if b.innerPerfEvent != nil {
		_ = b.innerPerfEvent.detach()
	}

	return nil
}

// Loaded checks bpf is still loaded.
func (b *defaultBPF) Loaded() (bool, error) {
	return true, nil
}

// EventPipe gets event-pipe and returns a PerfEventReader.
func (b *defaultBPF) EventPipe(ctx context.Context, mapID, perCPUBuffer uint32) (PerfEventReader, error) {
	reader, err := newPerfEventReader(ctx, b.mapSpecs[mapID].bMap, int(perCPUBuffer))
	if err != nil {
		return nil, err
	}

	log.Infof("event-pipe %d, perCPUBuffer %d", mapID, perCPUBuffer)
	return reader, nil
}

// EventPipeByName gets event-pipe by the mapName and returns a PerfEventReader.
func (b *defaultBPF) EventPipeByName(ctx context.Context, mapName string, perCPUBuffer uint32) (PerfEventReader, error) {
	return b.EventPipe(ctx, b.MapIDByName(mapName), perCPUBuffer)
}

// AttachAndEventPipe attaches and event-pipe and returns a PerfEventReader.
func (b *defaultBPF) AttachAndEventPipe(ctx context.Context, mapName string, perCPUBuffer uint32) (PerfEventReader, error) {
	reader, err := b.EventPipeByName(ctx, mapName, perCPUBuffer)
	if err != nil {
		return nil, err
	}

	if err := b.Attach(); err != nil {
		reader.Close()
		return nil, err
	}

	log.Infof("attach and event-pipe %s, perCPUBuffer %d", mapName, perCPUBuffer)
	return reader, nil
}

// ReadMap read the value content corresponding to a key from a map
//
// NOTICE: The content of the key needs to be converted to byte type, and the
// obtained value is of byte type, which also needs to be converted to the
// corresponding type.
func (b *defaultBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	val, err := b.mapSpecs[mapID].bMap.LookupBytes(key)
	if err != nil {
		return nil, err
	}

	log.Debugf("read map %d, key %v, value %v", mapID, key, val)
	return val, nil
}

// WriteMapItems write the value content corresponding to a key to a map.
func (b *defaultBPF) WriteMapItems(mapID uint32, items []MapItem) error {
	m := b.mapSpecs[mapID].bMap

	for _, item := range items {
		if err := m.Update(item.Key, item.Value, ebpf.UpdateAny); err != nil {
			return fmt.Errorf("map %d, key %v: update: %w", mapID, item.Key, err)
		}
		log.Infof("write map %d, key %v, value %v", mapID, item.Key, item.Value)
	}
	return nil
}

// DeleteMapItems deletes multiple items from a BPF map by keys.
func (b *defaultBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	m := b.mapSpecs[mapID].bMap

	for _, k := range keys {
		if err := m.Delete(k); err != nil {
			return fmt.Errorf("map %d, key %v: delete: %w", mapID, k, err)
		}
		log.Infof("delete map %d, key %v", mapID, k)
	}
	return nil
}

// DumpMap dump all the context of the map
func (b *defaultBPF) DumpMap(mapID uint32) ([]MapItem, error) {
	m := b.mapSpecs[mapID].bMap

	var prevKey any
	items := []MapItem{}
	for i := 0; i < int(m.MaxEntries()); i++ {
		nextKey, err := m.NextKeyBytes(prevKey)
		if err != nil {
			return nil, fmt.Errorf("map %d, prevKey %v: next key: %w", mapID, prevKey, err)
		}

		// last key
		if len(nextKey) == 0 {
			break
		}

		value, err := m.LookupBytes(nextKey)
		if err != nil {
			return nil, fmt.Errorf("map %d, key %v: value: %w", mapID, nextKey, err)
		}

		if value == nil {
			continue
		}

		prevKey = nextKey
		items = append(items, MapItem{
			Key:   nextKey,
			Value: value,
		})
	}

	log.Debugf("dump map %d, items %v", mapID, items)
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
