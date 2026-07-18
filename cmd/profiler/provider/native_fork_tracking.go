// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"encoding/binary"
	"fmt"
	"math"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/forktrack"
)

const (
	programProfilerFork = "profiler_fork"
	programProfilerExit = "profiler_exit"
	programProfilerExec = "profiler_exec"
	symbolProfilerFork  = "sched_process_fork"
	symbolProfilerExit  = "sched_process_exit"
	symbolProfilerExec  = "sched/sched_process_exec"
)

func nativeForkConfig(pctx *pcontext.ProfilerContext) (forktrack.Config, error) {
	if pctx.PID() < 0 {
		return forktrack.Config{}, fmt.Errorf("invalid root PID %d", pctx.PID())
	}
	for name, value := range map[string]uint{
		"fork-max-procs": pctx.ForkMaxProcesses,
		"fork-rate":      pctx.ForkRate,
		"fork-burst":     pctx.ForkBurst,
	} {
		if uint64(value) > math.MaxUint32 {
			return forktrack.Config{}, fmt.Errorf("--%s value %d exceeds uint32", name, value)
		}
	}
	config := forktrack.Config{
		Enabled:    pctx.FollowForks,
		RootPID:    uint32(pctx.PID()),
		MaxTracked: uint32(pctx.ForkMaxProcesses),
		Rate:       uint32(pctx.ForkRate),
		Burst:      uint32(pctx.ForkBurst),
	}
	if err := config.Validate(); err != nil {
		return forktrack.Config{}, fmt.Errorf("invalid fork tracking configuration: %w", err)
	}
	return config, nil
}

func applyNativeForkTracking(
	constants map[string]any,
	attachOpts []bpf.AttachOption,
	config forktrack.Config,
) (map[string]any, []bpf.AttachOption, error) {
	merged, err := config.MergeConstants(constants)
	if err != nil {
		return nil, nil, err
	}
	if !config.Enabled {
		return merged, attachOpts, nil
	}
	result := make([]bpf.AttachOption, 0, len(attachOpts)+3)
	result = append(result,
		bpf.AttachOption{ProgramName: programProfilerExit, Symbol: symbolProfilerExit},
		bpf.AttachOption{ProgramName: programProfilerExec, Symbol: symbolProfilerExec},
		bpf.AttachOption{ProgramName: programProfilerFork, Symbol: symbolProfilerFork},
	)
	// Attach lifecycle hooks first so a child created while later sampling
	// hooks are being attached is already in the tracked set.
	result = append(result, attachOpts...)
	return merged, result, nil
}

func loadNativeProfilerBPF(objectFile string, constants map[string]any, config forktrack.Config) (bpf.BPF, error) {
	return bpf.LoadBpfWithMapSizes(objectFile, constants, nativeForkMapSizes(config))
}

func nativeForkMapSizes(config forktrack.Config) map[string]uint32 {
	maxEntries := uint32(1)
	if config.Enabled {
		maxEntries = config.MaxTracked
	}
	return map[string]uint32{
		forktrack.PIDMapName: maxEntries,
	}
}

func logNativeForkStats(loaded bpf.BPF, enabled bool) {
	if !enabled || loaded == nil {
		return
	}
	mapID := loaded.MapIDByName(forktrack.StatsMapName)
	if mapID == 0 {
		log.Warn("fork tracking statistics map not found")
		return
	}
	key := make([]byte, 4)
	binary.NativeEndian.PutUint32(key, 0)
	data, err := loaded.ReadMap(mapID, key)
	if err != nil {
		log.Warn("failed to read fork tracking statistics", "error", err)
		return
	}
	stats, err := forktrack.DecodeStats(data, binary.NativeEndian)
	if err != nil {
		log.Warn("failed to decode fork tracking statistics", "error", err)
		return
	}
	if forktrack.Assess(true, stats) == forktrack.HealthLimited {
		log.Warn("fork tracking coverage was limited", "summary", forktrack.Summary(true, stats))
		return
	}
	log.Info("fork tracking stopped", "summary", forktrack.Summary(true, stats))
}

func stopNativeProfilerBPF(loaded bpf.BPF, forkTrackingEnabled bool) error {
	if loaded == nil {
		return nil
	}
	// Freeze all counters before reading the summary. Maps remain readable after
	// links are detached and until Close releases the collection.
	if err := loaded.Detach(); err != nil {
		log.Warn("detaching native profiler programs", "error", err)
	}
	logNativeForkStats(loaded, forkTrackingEnabled)
	return closeBpfSafe(loaded)
}
