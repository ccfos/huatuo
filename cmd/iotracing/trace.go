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

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/pkg/types"
)

// runTrace loads the BPF object, attaches probes for cfg.durationSecond
// seconds, then dumps and aggregates io_source_map. The duration timeout
// and signal handler are scoped to this call so the BPF object is always
// detached and closed before returning.
func runTrace(ctx context.Context, bpfPath string, cfg ioConfig, filters map[string]any) (*types.IOTracingReport, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read bpf object: %w", err)
	}

	b, err := bpf.LoadBpfFromBytes(bpfPath, bpfBytes, filters)
	if err != nil {
		return nil, fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	timeCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.durationSecond)*time.Second)
	defer cancel()

	signalCtx, signalCancel := signal.NotifyContext(timeCtx, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	defer signalCancel()

	reader, err := attachAndEventPipe(signalCtx, b)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	stalls, err := collectStalls(reader, cfg.maxStack)
	if err != nil {
		return nil, err
	}

	if err := b.Detach(); err != nil {
		return nil, fmt.Errorf("detach bpf: %w", err)
	}

	processes, err := dumpAndAggregate(b, cfg)
	if err != nil {
		return nil, err
	}

	return &types.IOTracingReport{Processes: processes, StallStacks: stalls}, nil
}

// collectStalls drains the iodelay perf reader until ctx cancellation
// and returns the most recent maxStack samples in chronological order.
// A ring buffer is used so that long traces don't lose the events that
// are usually most relevant for diagnosis — the ones closest to the end
// of the window.
func collectStalls(reader bpf.PerfEventReader, maxStack uint64) ([]types.IOScheduleEvent, error) {
	var (
		event bpfScheduleDelay
		ring  []types.IOScheduleEvent
		head  uint64
		count uint64
	)

	if maxStack > 0 {
		ring = make([]types.IOScheduleEvent, maxStack)
	}

	for {
		if err := reader.ReadInto(&event); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				break
			}

			return nil, fmt.Errorf("read event: %w", err)
		}

		if maxStack == 0 {
			continue
		}

		hostname, _ := executil.HostnameByPid(event.Pid)

		ring[head] = types.IOScheduleEvent{
			Comm:              bytesutil.ToStr(event.Comm[:]),
			ContainerHostname: hostname,
			Pid:               event.Pid,
			LatencyUs:         event.LatencyNs / 1000,
			Stack:             symbol.KsymStackStrs(event.Stack[:], symbol.KsymStackMinDepth),
		}

		head = (head + 1) % maxStack
		if count < maxStack {
			count++
		}
	}

	if count < maxStack {
		return ring[:count], nil
	}

	// Buffer wrapped: head points to the oldest entry. Stitch
	// [head:] + [:head] so output stays oldest-to-newest.
	out := make([]types.IOScheduleEvent, 0, maxStack)
	out = append(out, ring[head:]...)
	out = append(out, ring[:head]...)

	return out, nil
}

// dumpAndAggregate reads io_source_map after detach and reduces it into
// per-process stats, ordered by total block IO and capped by
// cfg.maxProcess / cfg.maxFilesPerProcess.
func dumpAndAggregate(b bpf.BPF, cfg ioConfig) ([]types.ProcessFileIOStats, error) {
	iodata, err := b.DumpMapByName(bpfSourceMapName)
	if err != nil {
		return nil, fmt.Errorf("dump %s: %w", bpfSourceMapName, err)
	}

	table := make(ProcessIOTable)

	for _, dataRaw := range iodata {
		var record bpfFilesystemIO

		if err := binary.Read(bytes.NewReader(dataRaw.Value), binary.LittleEndian, &record); err != nil {
			return nil, fmt.Errorf("decode %s entry: %w", bpfSourceMapName, err)
		}

		blkSize := record.BlockWriteBytes + record.BlockReadBytes
		table.Add(record.Pid, &fileEntry{Record: &record, Size: blkSize})
	}

	groups := table.TopN(int(cfg.maxProcess))

	processes := make([]types.ProcessFileIOStats, 0, len(groups))
	for _, g := range groups {
		processes = append(processes, buildProcessFileIOStats(g, cfg))
	}

	return processes, nil
}
