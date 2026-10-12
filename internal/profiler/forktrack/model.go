// Copyright 2026 The HuaTuo Authors
// SPDX-License-Identifier: Apache-2.0

package forktrack

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Process is the user-space representation of fork_pid_map. The model is used
// by diagnostics and deterministic tests for the same inheritance and cleanup
// rules implemented in eBPF.
type Process struct {
	PID        uint32
	RootPID    uint32
	ParentPID  uint32
	Generation uint32
	BornAt     time.Time
	Thread     bool
}

// RejectReason describes why a descendant could not be admitted.
type RejectReason string

const (
	RejectNone      RejectReason = ""
	RejectDisabled  RejectReason = "disabled"
	RejectUntracked RejectReason = "untracked_parent"
	RejectDuplicate RejectReason = "duplicate"
	RejectLimit     RejectReason = "process_limit"
	RejectRate      RejectReason = "rate_limit"
	RejectInvalid   RejectReason = "invalid_pid"
)

// Decision makes fork handling observable without requiring callers to infer
// behavior from counters.
type Decision struct {
	Accepted bool
	Reason   RejectReason
	Process  Process
}

// Stats mirrors the fixed layout of struct profiler_fork_stats_t. Keep fields
// in this order: telemetry.go decodes raw native-endian map bytes by offset.
type Stats struct {
	Active            uint64
	Accepted          uint64
	Duplicate         uint64
	UpdateFailures    uint64
	Exited            uint64
	RejectedLimit     uint64
	RejectedRate      uint64
	WindowStartNS     uint64
	WindowEvents      uint64
	DeepestGeneration uint64
	ExecMigrations    uint64
	RootExited        uint64
}

// Model provides a concurrency-safe reference implementation of the kernel
// lifecycle state machine. It is intentionally independent of /proc polling:
// input events are expected to arrive from sched_process_fork/exit.
type Model struct {
	mu        sync.RWMutex
	config    Config
	processes map[uint32]Process
	stats     Stats
	windowAt  time.Time
	windowN   uint64
}

func NewModel(config Config) (*Model, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Model{
		config:    config,
		processes: make(map[uint32]Process),
	}, nil
}

// Fork applies one scheduler fork event. Threads are represented by their TID,
// which is important because a worker thread can later fork a process.
func (m *Model) Fork(parentPID, childPID uint32, at time.Time) Decision {
	return m.ForkTask(parentPID, parentPID, childPID, childPID, at)
}

// ForkFrom also accepts the parent's thread-group ID. Scheduler tracepoints
// report parent_pid as a TID, so this recognizes worker threads that existed
// before tracking was attached and therefore have no descendant-map entry.
func (m *Model) ForkFrom(parentPID, parentTGID, childPID uint32, at time.Time) Decision {
	return m.ForkTask(parentPID, parentTGID, childPID, childPID, at)
}

// ForkTask models the raw scheduler event, including the child's TGID. A child
// whose PID differs from its TGID is a thread and inherits its process's
// generation; a new process advances one generation.
func (m *Model) ForkTask(parentPID, parentTGID, childPID, childTGID uint32, at time.Time) Decision {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		return Decision{Reason: RejectDisabled}
	}
	if parentPID == 0 || parentTGID == 0 || childPID == 0 || childTGID == 0 || parentPID == childPID {
		return Decision{Reason: RejectInvalid}
	}

	parentGeneration := uint32(0)
	lineageParentPID := uint32(0)
	if parentPID == m.config.RootPID || parentTGID == m.config.RootPID {
		if m.stats.RootExited != 0 {
			return Decision{Reason: RejectUntracked}
		}
	} else {
		parent, ok := m.processes[parentPID]
		if !ok && parentTGID != parentPID {
			parent, ok = m.processes[parentTGID]
		}
		if !ok {
			return Decision{Reason: RejectUntracked}
		}
		parentGeneration = parent.Generation
		lineageParentPID = parent.ParentPID
	}

	if existing, ok := m.processes[childPID]; ok {
		m.stats.Duplicate++
		return Decision{Reason: RejectDuplicate, Process: existing}
	}
	if uint64(len(m.processes)) >= uint64(m.config.MaxTracked) {
		m.stats.RejectedLimit++
		return Decision{Reason: RejectLimit}
	}
	if !m.allowEventLocked(at) {
		m.stats.RejectedRate++
		return Decision{Reason: RejectRate}
	}

	thread := childPID != childTGID
	process := Process{
		PID:        childPID,
		RootPID:    m.config.RootPID,
		ParentPID:  lineageParentPID,
		Generation: parentGeneration,
		BornAt:     at,
		Thread:     thread,
	}
	if !thread {
		process.ParentPID = parentTGID
		process.Generation++
	}
	m.processes[childPID] = process
	m.stats.Active = uint64(len(m.processes))
	m.stats.Accepted++
	if uint64(process.Generation) > m.stats.DeepestGeneration {
		m.stats.DeepestGeneration = uint64(process.Generation)
	}
	return Decision{Accepted: true, Process: process}
}

func (m *Model) allowEventLocked(at time.Time) bool {
	if m.config.Rate == 0 {
		return true
	}
	if m.windowAt.IsZero() || at.Before(m.windowAt) || at.Sub(m.windowAt) >= Window {
		m.windowAt = at
		m.windowN = 0
	}
	m.windowN++
	m.stats.WindowStartNS = uint64(m.windowAt.UnixNano())
	m.stats.WindowEvents = m.windowN
	return m.windowN <= m.config.EffectiveAllowance()
}

// Exit removes one tracked TID/PID. Exiting the configured root does not clear
// descendants: those entries are independent and continue to be sampled.
func (m *Model) Exit(pid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pid == m.config.RootPID {
		m.stats.RootExited = 1
		return false
	}
	if _, ok := m.processes[pid]; !ok {
		return false
	}
	delete(m.processes, pid)
	m.stats.Active = uint64(len(m.processes))
	m.stats.Exited++
	return true
}

// Exec migrates a tracked record when a non-leader thread execs and assumes
// the thread-group leader PID. Linux exposes both values through
// sched_process_exec as pid and old_pid.
func (m *Model) Exec(pid, oldPID uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.config.Enabled || pid == 0 || oldPID == 0 || pid == oldPID {
		return false
	}
	oldProcess, ok := m.processes[oldPID]
	if !ok {
		return false
	}
	if pid == m.config.RootPID {
		delete(m.processes, oldPID)
		m.stats.Active = uint64(len(m.processes))
		m.stats.ExecMigrations++
		return true
	}
	if _, targetExists := m.processes[pid]; !targetExists {
		oldProcess.PID = pid
		oldProcess.Thread = false
		m.processes[pid] = oldProcess
	}
	delete(m.processes, oldPID)
	m.stats.Active = uint64(len(m.processes))
	m.stats.ExecMigrations++
	return true
}

// Tracked returns true for the root and every admitted descendant. The root is
// implicit and therefore does not consume MaxTracked capacity.
func (m *Model) Tracked(pid uint32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.Enabled {
		return m.config.RootPID == 0 || pid == m.config.RootPID
	}
	if pid == m.config.RootPID {
		return m.stats.RootExited == 0
	}
	_, ok := m.processes[pid]
	return ok
}

func (m *Model) Process(pid uint32) (Process, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	process, ok := m.processes[pid]
	return process, ok
}

func (m *Model) Snapshot() []Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Process, 0, len(m.processes))
	for _, process := range m.processes {
		result = append(result, process)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Generation == result[j].Generation {
			return result[i].PID < result[j].PID
		}
		return result[i].Generation < result[j].Generation
	})
	return result
}

func (m *Model) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

func (m *Model) ResetCounters() {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := uint64(len(m.processes))
	rootExited := m.stats.RootExited
	m.stats = Stats{Active: active, RootExited: rootExited}
	m.windowAt = time.Time{}
	m.windowN = 0
}

// CheckInvariants catches corrupted snapshots before they are exposed in
// diagnostics. Parent entries may legitimately be absent after parent exit.
func (m *Model) CheckInvariants() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stats.Active != uint64(len(m.processes)) {
		return fmt.Errorf("active counter %d does not match %d tracked entries", m.stats.Active, len(m.processes))
	}
	if uint64(len(m.processes)) > uint64(m.config.MaxTracked) {
		return fmt.Errorf("tracked entries %d exceed configured maximum %d", len(m.processes), m.config.MaxTracked)
	}
	for pid, process := range m.processes {
		if pid == 0 || process.PID != pid {
			return fmt.Errorf("invalid process key %d for record PID %d", pid, process.PID)
		}
		if process.RootPID != m.config.RootPID {
			return fmt.Errorf("PID %d inherited root %d, expected %d", pid, process.RootPID, m.config.RootPID)
		}
		if process.Generation == 0 && !process.Thread {
			return fmt.Errorf("non-thread PID %d has zero generation", pid)
		}
	}
	return nil
}
