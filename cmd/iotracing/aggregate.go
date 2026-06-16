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
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/pkg/types"
)

// buildProcessFileIOStats reduces one pidGroup into a ProcessFileIOStats:
// every entry contributes to the per-pid totals, while only the first
// cfg.maxFilesPerProcess (the highest-IO files, since g.Files is pre-sorted)
// produce structured per-file rows. cfg.durationSecond converts raw byte
// counters to per-second rates.
func buildProcessFileIOStats(g *pidGroup, cfg ioConfig) types.ProcessFileIOStats {
	var read, write, dread, dwrite uint64
	var fileStats []types.FileIOStats
	var comm string

	for i, fe := range g.Files {
		record := fe.Record

		wbps := record.FsWriteBytes / cfg.durationSecond
		rbps := record.FsReadBytes / cfg.durationSecond
		dwbps := record.BlockWriteBytes / cfg.durationSecond
		drbps := record.BlockReadBytes / cfg.durationSecond

		read += rbps
		write += wbps
		dread += drbps
		dwrite += dwbps

		// First (highest-IO) record's comm is the fallback when
		// /proc/<pid>/comm can't be read.
		if comm == "" {
			comm = bytesutil.ToStr(record.Comm[:])
		}

		if uint64(i) >= cfg.maxFilesPerProcess {
			continue
		}

		var q2c, d2c, maxQ2C, maxD2C uint64
		if record.Latency.Count > 0 {
			q2c = record.Latency.SumQ2CNs / (record.Latency.Count * 1000)
			d2c = record.Latency.SumD2CNs / (record.Latency.Count * 1000)
			maxQ2C = record.Latency.MaxQ2CNs / 1000
			maxD2C = record.Latency.MaxD2CNs / 1000
		}

		fileStats = append(fileStats, types.FileIOStats{
			Major:        record.DevID >> 20 & 0xfff,
			Minor:        record.DevID & 0xfffff,
			Inode:        record.Ino,
			Path:         record.PathName(),
			IsDirect:     record.IsDirect(),
			FsReadBps:    rbps,
			FsWriteBps:   wbps,
			DiskReadBps:  drbps,
			DiskWriteBps: dwbps,
			Q2CUs:        q2c,
			D2CUs:        d2c,
			MaxQ2CUs:     maxQ2C,
			MaxD2CUs:     maxD2C,
		})
	}

	cmdline, err := executil.ProcNameByPid(g.Pid)
	if err != nil {
		cmdline = comm
	}

	out := types.ProcessFileIOStats{
		Pid:               g.Pid,
		Comm:              cmdline,
		TotalFsReadBps:    read,
		TotalFsWriteBps:   write,
		TotalDiskReadBps:  dread,
		TotalDiskWriteBps: dwrite,
		TotalFiles:        fileStats,
		TotalFileCount:    uint64(len(g.Files)),
	}
	out.ContainerHostname, _ = executil.HostnameByPid(g.Pid)

	return out
}
