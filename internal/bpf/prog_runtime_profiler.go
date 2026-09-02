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
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

const (
	// ProgRuntimeProfilerName is the tracer name.
	ProgRuntimeProfilerName = "bpf_prog_runtime"

	// progRuntimeObjectName is the compiled profiler BPF object name.
	progRuntimeObjectName     = "bpf_prog_runtime.o"
	progRuntimeStatsMapName   = "profile_stats"
	progRuntimeFentryProgName = "profile_fentry"
	progRuntimeFexitProgName  = "profile_fexit"
)

var errProgRuntimeClosed = errors.New("bpf: program runtime profiler is closed")

// progRuntimeStats contains cumulative statistics for one target BPF program.
type progRuntimeStats struct {
	RunTimeNS uint64
	RunCount  uint64
}

type progRuntimeProfiler struct {
	mu         sync.RWMutex
	closed     bool
	target     *ebpf.Program
	collection *ebpf.Collection
	statsMap   *ebpf.Map
	fentryLink link.Link
	fexitLink  link.Link
}

func newBPFProgRuntimeByID(programID uint32, objectPath string) (*progRuntimeProfiler, error) {
	if programID == 0 {
		return nil, fmt.Errorf("program ID must be greater than zero")
	}
	if objectPath == "" {
		return nil, fmt.Errorf("profiler object path must not be empty")
	}

	target, err := ebpf.NewProgramFromID(ebpf.ProgramID(programID))
	if err != nil {
		return nil, fmt.Errorf("open target BPF program %d: %w", programID, err)
	}
	progName, err := targetProgramName(target)
	if err != nil {
		_ = target.Close()
		return nil, fmt.Errorf("resolve target BPF program %d: %w", programID, err)
	}
	return newProgRuntimeProfiler(target, ebpf.ProgramID(programID), progName, objectPath)
}

func newProgRuntimeProfiler(target *ebpf.Program, programID ebpf.ProgramID, progName, objectPath string) (*progRuntimeProfiler, error) {
	var collection *ebpf.Collection
	var fentryLink link.Link
	var fexitLink link.Link
	loaded := false
	defer func() {
		if loaded {
			return
		}
		if fexitLink != nil {
			_ = fexitLink.Close()
		}
		if fentryLink != nil {
			_ = fentryLink.Close()
		}
		if collection != nil {
			collection.Close()
		}
		_ = target.Close()
	}()

	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", progRuntimeObjectName, err)
	}

	for _, name := range []string{progRuntimeFentryProgName, progRuntimeFexitProgName} {
		program, ok := spec.Programs[name]
		if !ok {
			return nil, fmt.Errorf("load %s: program %q not found", progRuntimeObjectName, name)
		}
		program.AttachTarget = target
		program.AttachTo = progName
	}

	collection, err = ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load targeted profiler for program %d: %w", programID, err)
	}

	statsMap, ok := collection.Maps[progRuntimeStatsMapName]
	if !ok {
		return nil, fmt.Errorf("load %s: map %q not found", progRuntimeObjectName, progRuntimeStatsMapName)
	}

	fentryProgram, ok := collection.Programs[progRuntimeFentryProgName]
	if !ok {
		return nil, fmt.Errorf("load %s: program %q not found", progRuntimeObjectName, progRuntimeFentryProgName)
	}
	fexitProgram, ok := collection.Programs[progRuntimeFexitProgName]
	if !ok {
		return nil, fmt.Errorf("load %s: program %q not found", progRuntimeObjectName, progRuntimeFexitProgName)
	}

	fentryLink, err = link.AttachTracing(link.TracingOptions{
		Program:    fentryProgram,
		AttachType: ebpf.AttachTraceFEntry,
	})
	if err != nil {
		return nil, fmt.Errorf("attach fentry to program %d: %w", programID, err)
	}

	fexitLink, err = link.AttachTracing(link.TracingOptions{
		Program:    fexitProgram,
		AttachType: ebpf.AttachTraceFExit,
	})
	if err != nil {
		return nil, fmt.Errorf("attach fexit to program %d: %w", programID, err)
	}

	profiler := &progRuntimeProfiler{
		target:     target,
		collection: collection,
		statsMap:   statsMap,
		fentryLink: fentryLink,
		fexitLink:  fexitLink,
	}
	loaded = true
	return profiler, nil
}

func targetProgramName(target *ebpf.Program) (string, error) {
	info, err := target.Info()
	if err != nil {
		return "", fmt.Errorf("get program info: %w", err)
	}
	if _, ok := info.BTFID(); !ok {
		return "", fmt.Errorf("target program has no BTF information")
	}

	instructions, err := info.Instructions()
	if err != nil {
		return "", fmt.Errorf("get program function info: %w", err)
	}
	if name := instructions.Name(); name != "" {
		return name, nil
	}

	return "", fmt.Errorf("target program function name is empty")
}

// Read aggregates per-CPU counters.
func (p *progRuntimeProfiler) Read() (progRuntimeStats, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return progRuntimeStats{}, errProgRuntimeClosed
	}

	key := uint32(0)
	var values []abi.BPFProgRuntimeValue
	if err := p.statsMap.Lookup(&key, &values); err != nil {
		return progRuntimeStats{}, fmt.Errorf("read profile stats: %w", err)
	}

	return aggregateProgRuntimeStats(values), nil
}

func aggregateProgRuntimeStats(values []abi.BPFProgRuntimeValue) progRuntimeStats {
	var stats progRuntimeStats
	for _, value := range values {
		stats = addProgRuntimeStats(stats, progRuntimeStats{
			RunTimeNS: value.RunTimeNS,
			RunCount:  value.RunCount,
		})
	}
	return stats
}

func addProgRuntimeStats(total, delta progRuntimeStats) progRuntimeStats {
	total.RunTimeNS = saturatingAdd(total.RunTimeNS, delta.RunTimeNS)
	total.RunCount = saturatingAdd(total.RunCount, delta.RunCount)
	return total
}

func saturatingAdd(a, b uint64) uint64 {
	if b > math.MaxUint64-a {
		return math.MaxUint64
	}
	return a + b
}

// Close detaches the profiler and releases all kernel resources.
func (p *progRuntimeProfiler) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	err := errors.Join(p.fentryLink.Close(), p.fexitLink.Close())
	p.collection.Close()
	err = errors.Join(err, p.target.Close())
	return err
}

type progRuntimeProfile interface {
	Read() (progRuntimeStats, error)
	Close() error
}

type bpfProgRuntimeInstance struct {
	name    string
	profile progRuntimeProfile
	last    progRuntimeStats
}

// ProgRuntimeMetric is one sample exported for a known BPF program.
type ProgRuntimeMetric struct {
	ProgramName    string
	RunTimeNS      uint64
	RunCount       uint64
	Up             bool
	AttachFailures uint64
}

type progRuntimeManager struct {
	sync.Mutex
	enabled        bool
	all            bool
	targets        map[string]struct{}
	knownNames     map[string]struct{}
	active         map[*bpfProgRuntimeInstance]struct{}
	history        map[string]progRuntimeStats
	attachFailures map[string]uint64
	newProfile     func(uint32, string) (progRuntimeProfile, error)
}

func newProgRuntimeManager(newProfile func(uint32, string) (progRuntimeProfile, error)) *progRuntimeManager {
	return &progRuntimeManager{
		targets:        make(map[string]struct{}),
		knownNames:     make(map[string]struct{}),
		active:         make(map[*bpfProgRuntimeInstance]struct{}),
		history:        make(map[string]progRuntimeStats),
		attachFailures: make(map[string]uint64),
		newProfile:     newProfile,
	}
}

var progRuntime = newProgRuntimeManager(func(programID uint32, objectPath string) (progRuntimeProfile, error) {
	return newBPFProgRuntimeByID(programID, objectPath)
})

func setProgRuntimeProfiler(opts ProgRuntimeOptions) error {
	if !opts.Enabled {
		return progRuntime.configure(false, false, nil)
	}

	programNames, err := normalizeProgRuntimeTargets(opts.Targets)
	if err != nil {
		return err
	}
	return progRuntime.configure(true, len(programNames) == 0, programNames)
}

func normalizeProgRuntimeTargets(targets []string) ([]string, error) {
	names := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for i, target := range targets {
		name := strings.TrimSpace(target)
		if name == "" {
			return nil, fmt.Errorf("%s: target %d program name must not be empty", ProgRuntimeProfilerName, i)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("%s: duplicate program name %q", ProgRuntimeProfilerName, name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func (m *progRuntimeManager) configure(enabled, all bool, programNames []string) error {
	m.Lock()
	defer m.Unlock()

	if len(m.active) != 0 {
		return fmt.Errorf("cannot configure BPF program runtime profiler while programs are active")
	}

	m.enabled = enabled
	m.all = all
	m.targets = make(map[string]struct{}, len(programNames))
	m.knownNames = make(map[string]struct{}, len(programNames))
	m.history = make(map[string]progRuntimeStats)
	m.attachFailures = make(map[string]uint64)
	for _, name := range programNames {
		m.targets[name] = struct{}{}
		m.knownNames[name] = struct{}{}
	}
	return nil
}

func (m *progRuntimeManager) selected(name string) bool {
	if !m.enabled {
		return false
	}
	if m.all {
		return true
	}
	_, ok := m.targets[name]
	return ok
}

func startBPFProgRuntimeProfiles(programs []ProgramInfo) []*bpfProgRuntimeInstance {
	return progRuntime.start(programs, filepath.Join(DefaultObjDir, progRuntimeObjectName))
}

func stopBPFProgRuntimeProfiles(instances []*bpfProgRuntimeInstance) error {
	return progRuntime.stop(instances)
}

func (m *progRuntimeManager) start(programs []ProgramInfo, objectPath string) []*bpfProgRuntimeInstance {
	m.Lock()
	defer m.Unlock()

	instances := make([]*bpfProgRuntimeInstance, 0, len(programs))
	for _, program := range programs {
		if !m.selected(program.Name) {
			continue
		}

		instance := &bpfProgRuntimeInstance{name: program.Name}
		m.knownNames[program.Name] = struct{}{}
		profile, err := m.newProfile(program.ID, objectPath)
		if err != nil {
			m.attachFailures[program.Name]++
			log.Errorf("%s: attach target BPF program %q (ID %d): %v", ProgRuntimeProfilerName, program.Name, program.ID, err)
		} else {
			instance.profile = profile
		}
		m.active[instance] = struct{}{}
		instances = append(instances, instance)
	}
	return instances
}

func (m *progRuntimeManager) stop(instances []*bpfProgRuntimeInstance) error {
	m.Lock()
	defer m.Unlock()

	var errs []error
	for _, instance := range instances {
		if _, ok := m.active[instance]; !ok {
			continue
		}
		delete(m.active, instance)
		if instance.profile == nil {
			continue
		}

		stats, err := instance.profile.Read()
		if err != nil {
			stats = instance.last
			errs = append(errs, fmt.Errorf("final read of target BPF program %q: %w", instance.name, err))
		}
		m.history[instance.name] = addProgRuntimeStats(
			m.history[instance.name],
			stats,
		)
		if err := instance.profile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close profiler for target BPF program %q: %w", instance.name, err))
		}
	}
	return errors.Join(errs...)
}

func ReadBPFProgRuntimeData() []ProgRuntimeMetric {
	return progRuntime.getData()
}

func (m *progRuntimeManager) getData() []ProgRuntimeMetric {
	m.Lock()
	defer m.Unlock()

	totals := make(map[string]progRuntimeStats, len(m.knownNames))
	up := make(map[string]bool, len(m.knownNames))
	active := make(map[string]int, len(m.knownNames))
	for name := range m.knownNames {
		totals[name] = m.history[name]
	}

	for instance := range m.active {
		active[instance.name]++
		if active[instance.name] == 1 {
			up[instance.name] = true
		}
		if instance.profile == nil {
			up[instance.name] = false
			continue
		}

		stats, err := instance.profile.Read()
		if err != nil {
			stats = instance.last
			up[instance.name] = false
			log.Errorf("%s: read target BPF program %q: %v", ProgRuntimeProfilerName, instance.name, err)
		} else {
			instance.last = stats
		}
		totals[instance.name] = addProgRuntimeStats(
			totals[instance.name],
			stats,
		)
	}

	names := slices.Sorted(maps.Keys(m.knownNames))

	data := make([]ProgRuntimeMetric, 0, len(names))
	for _, name := range names {
		data = append(data, ProgRuntimeMetric{
			ProgramName:    name,
			RunTimeNS:      totals[name].RunTimeNS,
			RunCount:       totals[name].RunCount,
			Up:             active[name] != 0 && up[name],
			AttachFailures: m.attachFailures[name],
		})
	}
	return data
}
