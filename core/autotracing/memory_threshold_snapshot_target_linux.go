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

package autotracing

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

	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	maxCgroupProcesses        = 4096
	maxCgroupProcessListBytes = 64 << 10
)

var (
	errProcessNotEligible     = errors.New("process is no longer eligible for a memory snapshot")
	errNoSnapshotProcess      = errors.New("cgroup has no OOM-killable process")
	errInvalidSnapshotProcess = errors.New("memory snapshot process binding is no longer valid")
)

type selectedProcess struct {
	identity    memsnapshot.ProcessIdentity
	comm        string
	oomScoreAdj int
}

type processCandidate struct {
	process     selectedProcess
	memoryBytes uint64
	score       float64
}

// procRoot must describe the same PID namespace used by the memory collector.
type processSelector struct {
	source   *cgroupSource
	procRoot string
}

// Select returns a process only after bounded enumeration and final validation.
// Membership and identity may change again after it returns.
func (s *processSelector) Select(ctx context.Context, group cgroupRef, memoryLimitBytes uint64) (selectedProcess, error) {
	if err := ctx.Err(); err != nil {
		return selectedProcess{}, err
	}
	procFS, err := procfs.NewFS(s.procRoot)
	if err != nil {
		return selectedProcess{}, fmt.Errorf("open procfs: %w", err)
	}
	process, err := selectProcessFromProcs(func(visit func(int) error) error {
		return s.scanProcesses(ctx, group, visit)
	}, func(pid int) (processCandidate, error) {
		proc, err := procFS.Proc(pid)
		if err != nil {
			return processCandidate{}, fmt.Errorf("open proc: %w", err)
		}
		stat, err := proc.Stat()
		if err != nil {
			return processCandidate{}, fmt.Errorf("read identity: %w", err)
		}
		oomScoreAdjRaw, err := os.ReadFile(filepath.Join(s.procRoot, strconv.Itoa(pid), "oom_score_adj"))
		if err != nil {
			return processCandidate{}, fmt.Errorf("read oom_score_adj: %w", err)
		}
		candidate, err := readProcessCandidate(proc, oomScoreAdjRaw, memoryLimitBytes)
		if err != nil {
			return processCandidate{}, err
		}
		current, err := proc.Stat()
		if err != nil {
			return processCandidate{}, fmt.Errorf("recheck identity: %w", err)
		}
		if stat.Starttime == 0 || current.Starttime != stat.Starttime {
			return processCandidate{}, errProcessNotEligible
		}
		candidate.process.identity = memsnapshot.ProcessIdentity{TGID: pid, StartTimeTicks: stat.Starttime}
		return candidate, nil
	})
	if err != nil {
		return selectedProcess{}, err
	}
	if err := s.Validate(ctx, group, process.identity); err != nil {
		return selectedProcess{}, err
	}

	return process, nil
}

func selectProcessFromProcs(scan func(func(int) error) error,
	read func(int) (processCandidate, error),
) (selectedProcess, error) {
	var selected processCandidate
	found := false
	err := scan(func(pid int) error {
		candidate, err := read(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) ||
			errors.Is(err, errProcessNotEligible) {
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
		return selectedProcess{}, fmt.Errorf("enumerate cgroup processes: %w", err)
	}
	if !found {
		return selectedProcess{}, errNoSnapshotProcess
	}
	return selected.process, nil
}

func (s *processSelector) Validate(ctx context.Context, group cgroupRef, identity memsnapshot.ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	found := false
	if err := s.scanProcesses(ctx, group, func(pid int) error {
		found = found || pid == identity.TGID
		return nil
	}); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: process %d left memory cgroup %q", errInvalidSnapshotProcess, identity.TGID, group.Path)
	}
	if err := memsnapshot.ValidateIdentity(s.procRoot, identity); err != nil {
		return fmt.Errorf("%w: %w", errInvalidSnapshotProcess, err)
	}

	return ctx.Err()
}

func readProcessCandidate(proc procfs.Proc, oomScoreAdjRaw []byte,
	memoryLimitBytes uint64,
) (processCandidate, error) {
	status, err := proc.NewStatus()
	if err != nil {
		return processCandidate{}, fmt.Errorf("read status: %w", err)
	}
	oomScoreAdj, err := strconv.Atoi(strings.TrimSpace(string(oomScoreAdjRaw)))
	if err != nil {
		return processCandidate{}, fmt.Errorf("parse oom_score_adj: %w", err)
	}
	if oomScoreAdj < -1000 || oomScoreAdj > 1000 {
		return processCandidate{}, fmt.Errorf("oom_score_adj out of range: %d", oomScoreAdj)
	}
	if oomScoreAdj == -1000 {
		return processCandidate{}, errProcessNotEligible
	}
	memoryBytes := oomMemoryBytes(status.VmRSS, status.VmSwap, status.VmPTE)
	score := float64(memoryBytes) + float64(oomScoreAdj)*float64(memoryLimitBytes)/1000
	if score < 0 {
		score = 0
	}
	return processCandidate{
		process:     selectedProcess{comm: status.Name, oomScoreAdj: oomScoreAdj},
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

func (s *processSelector) scanProcesses(ctx context.Context, group cgroupRef, visit func(int) error) error {
	if err := s.source.Validate(ctx, group); err != nil {
		return fmt.Errorf("%w: %w", errInvalidSnapshotProcess, err)
	}
	directory := s.source.memcgDir(group.Path)
	file, err := os.Open(filepath.Join(directory, "cgroup.procs"))
	if err != nil {
		return err
	}
	defer file.Close()
	if err := scanProcessPIDs(ctx, file, visit); err != nil {
		return err
	}
	if err := s.source.Validate(ctx, group); err != nil {
		return fmt.Errorf("%w: %w", errInvalidSnapshotProcess, err)
	}

	return nil
}

func scanProcessPIDs(ctx context.Context, reader io.Reader, visit func(int) error) error {
	limited := &io.LimitedReader{R: reader, N: maxCgroupProcessListBytes + 1}
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
		if count > maxCgroupProcesses || limited.N == 0 {
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
