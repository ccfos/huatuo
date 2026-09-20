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

package events

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	maxVictimPIDs      = 4096
	maxVictimListBytes = 64 << 10
)

var errNotOOMCandidate = errors.New("process is no longer an OOM candidate")

type victimCandidate struct {
	identity    memsnapshot.ProcessIdentity
	pid         int
	comm        string
	oomScoreAdj int
	memoryBytes uint64
	score       float64
}

func selectVictim(ctx context.Context, cgroupPath string, memoryMax uint64) (victimCandidate, error) {
	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return victimCandidate{}, fmt.Errorf("open procfs: %w", err)
	}
	return selectVictimFromProcs(func(visit func(int) error) error {
		return scanMemcgProcs(ctx, cgroupPath, visit)
	}, func(pid int) (victimCandidate, error) {
		identity, err := memsnapshot.ReadIdentity(pid)
		if err != nil {
			return victimCandidate{}, fmt.Errorf("read identity: %w", err)
		}
		proc, err := procFS.Proc(pid)
		if err != nil {
			return victimCandidate{}, fmt.Errorf("open proc: %w", err)
		}
		oomScoreAdjRaw, err := os.ReadFile(procfs.Path(strconv.Itoa(pid), "oom_score_adj"))
		if err != nil {
			return victimCandidate{}, fmt.Errorf("read oom_score_adj: %w", err)
		}
		candidate, err := readVictimCandidate(proc, oomScoreAdjRaw, memoryMax)
		if err != nil {
			return victimCandidate{}, err
		}
		current, err := memsnapshot.ReadIdentity(pid)
		if err != nil {
			return victimCandidate{}, fmt.Errorf("recheck identity: %w", err)
		}
		if current != identity {
			return victimCandidate{}, errNotOOMCandidate
		}
		candidate.identity = identity
		return candidate, nil
	})
}

func selectVictimFromProcs(scan func(func(int) error) error,
	read func(int) (victimCandidate, error),
) (victimCandidate, error) {
	var selected victimCandidate
	found := false
	err := scan(func(pid int) error {
		candidate, err := read(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) ||
			errors.Is(err, errNotOOMCandidate) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read pid %d: %w", pid, err)
		}
		if !found || candidate.score > selected.score ||
			(candidate.score == selected.score && candidate.memoryBytes > selected.memoryBytes) {
			selected, found = candidate, true
		}
		return nil
	})
	if err != nil {
		// Failed reads and enumeration must not select a winner from a partial view.
		return victimCandidate{}, fmt.Errorf("enumerate cgroup processes: %w", err)
	}
	if !found {
		return victimCandidate{}, errors.New("cgroup has no OOM-killable process")
	}
	return selected, nil
}

func validateVictim(ctx context.Context, cgroupPath string, identity memsnapshot.ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	found := false
	if err := scanMemcgProcs(ctx, cgroupPath, func(pid int) error {
		found = found || pid == identity.TGID
		return nil
	}); err != nil {
		return err
	}
	if !found {
		return errors.New("victim is no longer in the triggering memory cgroup")
	}
	return memsnapshot.ValidateIdentity("/proc", identity)
}

func readVictimCandidate(proc procfs.Proc, oomScoreAdjRaw []byte,
	memoryMax uint64,
) (victimCandidate, error) {
	status, err := proc.NewStatus()
	if err != nil {
		return victimCandidate{}, fmt.Errorf("read status: %w", err)
	}
	oomScoreAdj, err := strconv.Atoi(strings.TrimSpace(string(oomScoreAdjRaw)))
	if err != nil {
		return victimCandidate{}, fmt.Errorf("parse oom_score_adj: %w", err)
	}
	if oomScoreAdj < -1000 || oomScoreAdj > 1000 {
		return victimCandidate{}, fmt.Errorf("oom_score_adj out of range: %d", oomScoreAdj)
	}
	if oomScoreAdj == -1000 {
		return victimCandidate{}, errNotOOMCandidate
	}
	memoryBytes := oomMemoryBytes(status.VmRSS, status.VmSwap, status.VmPTE)
	score := float64(memoryBytes) + float64(oomScoreAdj)*float64(memoryMax)/1000
	if score < 0 {
		score = 0
	}
	return victimCandidate{
		pid: proc.PID, comm: status.Name, oomScoreAdj: oomScoreAdj,
		memoryBytes: memoryBytes, score: score,
	}, nil
}

func oomMemoryBytes(values ...uint64) uint64 {
	var total uint64
	for _, value := range values {
		if math.MaxUint64-total < value {
			return math.MaxUint64
		}
		total += value
	}
	return total
}

func scanMemcgProcs(ctx context.Context, cgroupPath string, visit func(int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var directory string
	switch mode := cgroups.CgroupMode(); mode {
	case cgroups.Legacy, cgroups.Hybrid:
		directory = paths.Path(subsystem.SubsystemMemory, cgroupPath)
	case cgroups.Unified:
		directory = paths.Path(cgroupPath)
	default:
		return fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	file, err := os.Open(filepath.Join(directory, "cgroup.procs"))
	if err != nil {
		return err
	}
	defer file.Close()
	return scanVictimPIDs(ctx, file, visit)
}

func scanVictimPIDs(ctx context.Context, reader io.Reader, visit func(int) error) error {
	limited := &io.LimitedReader{R: reader, N: maxVictimListBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 1024), 64)
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scanner.Scan() {
			break
		}
		count++
		if count > maxVictimPIDs || limited.N == 0 {
			return errors.New("cgroup process enumeration exceeds safety budget")
		}
		pid, err := strconv.Atoi(scanner.Text())
		if err != nil || pid <= 0 {
			return errors.New("invalid cgroup process ID")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(pid); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan cgroup processes: %w", err)
	}
	if limited.N == 0 {
		return errors.New("cgroup process list exceeds byte budget")
	}
	return ctx.Err()
}
