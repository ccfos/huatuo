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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnap"
	"github.com/ccfos/huatuo/internal/memsnap/collector"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	beforeOOMTracer        = "before_oom_memsnap"
	arbitrationDelay       = 20 * time.Millisecond
	victimSelectionTimeout = time.Second
	maxVictimPIDs          = 4096
	maxVictimListBytes     = 64 << 10
	inotifyBufferBytes     = 64 * 1024
	maxEpollEvents         = 64
	maxCgroupDirectories   = 8192
	maxWatchedCgroups      = 4096
	maxCgroupScanEntries   = 65536
	maxCgroupDepth         = 64
)

var containerCgroupIDRegexp = regexp.MustCompile(
	`^(?:([0-9a-f]{64})|(?:cri-containerd-|docker-|crio-)([0-9a-f]{64})\.scope)$`,
)

var errCgroupWatchLimit = errors.New("before-OOM cgroup discovery/watch safety limit reached")

var errNotOOMCandidate = errors.New("process is no longer an OOM candidate")

func isResourceExhaustion(err error) bool {
	return errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) ||
		errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.ENOMEM)
}

func handleWatchError(ctx context.Context, err error) error {
	if !isResourceExhaustion(err) {
		return err
	}
	log.Errorf("before-OOM memory snapshot stopped after resource exhaustion; "+
		"increase process FD/inotify limits and restart huatuo-bamai to re-enable it: %v", err)
	// Keep Start blocked until shutdown so the generic event runner does not
	// repeatedly rebuild and rescan the whole cgroup tree.
	<-ctx.Done()
	return nil
}

type beforeOOMMemsnap struct {
	captureOps  *beforeOOMOps
	cgroup      cgroups.Cgroup
	lastAttempt time.Time
}

type memcgCandidate struct {
	containerID string
	cgroupPath  string
	current     uint64
	max         uint64
	ratio       float64
}

type victimCandidate struct {
	identity    memsnap.ProcessIdentity
	pid         int
	comm        string
	oomScoreAdj int
	memoryBytes uint64
	score       float64
}

type beforeOOMData struct {
	CgroupPath         string            `json:"cgroup_path"`
	MemoryCurrent      uint64            `json:"memory_current"`
	MemoryMax          uint64            `json:"memory_max"`
	MemoryUsagePercent float64           `json:"memory_usage_percent"`
	VictimPID          int               `json:"victim_pid"`
	VictimProcessName  string            `json:"victim_process_name"`
	VictimOOMScoreAdj  int               `json:"victim_oom_score_adj"`
	Language           memsnap.Language  `json:"language"`
	Snapshot           *memsnap.Snapshot `json:"snapshot"`
}

func init() {
	tracing.RegisterEventTracing(beforeOOMTracer, newBeforeOOMMemsnap)
}

// The tracing registry invokes this factory only during its one-time
// initialization. Returning ErrNotSupported keeps a disabled tracer inactive;
// enabling it in a subsequently published config requires a process restart
// because the registry deliberately does not reconcile factories at runtime.
func newBeforeOOMMemsnap() (*tracing.EventTracingAttr, error) {
	if !configSnapshot().BeforeOOMMemsnap.Enabled {
		return nil, types.ErrNotSupported
	}

	return &tracing.EventTracingAttr{
		TracingData: &beforeOOMMemsnap{},
		Interval:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (s *beforeOOMMemsnap) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	cfg := configSnapshot().BeforeOOMMemsnap
	if err := validateBeforeOOMConfig(&cfg); err != nil {
		return fmt.Errorf("invalid before-OOM memory snapshot config: %w", err)
	}
	if s.cgroup == nil {
		cgroup, err := cgroups.NewManager()
		if err != nil {
			return fmt.Errorf("create cgroup manager: %w", err)
		}
		s.cgroup = cgroup
	}
	watcher, err := newPressureWatcher(s.cgroup, &cfg)
	if err != nil {
		return handleWatchError(ctx, err)
	}
	return handleWatchError(ctx, s.watchAndCapture(ctx, &cfg, watcher))
}

func (s *beforeOOMMemsnap) watchAndCapture(ctx context.Context,
	cfg *BeforeOOMConfig, watcher *pressureWatcher,
) error {
	watchCtx, cancel := context.WithCancel(ctx)
	events, watcherDone := watcher.Run(watchCtx)
	defer func() {
		cancel()
		// A restart must not overlap the previous watcher's FD ownership.
		<-watcherDone
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcherDone:
			return err
		case event, ok := <-events:
			if !ok {
				return <-watcherDone
			}
			now := time.Now()
			if !s.captureAllowed(cfg, now) {
				continue
			}
			candidate, ok, err := s.bestCaptureCandidate(ctx, cfg, events, event)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				log.Debugf("before-OOM pressure event skipped: %v", err)
				continue
			}
			if !ok {
				continue
			}
			err = s.captureCandidate(ctx, cfg, &candidate)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// Every completed attempt consumes the same node-wide cooldown.
			s.lastAttempt = time.Now()
			if err != nil {
				log.Warnf("before-OOM memory snapshot skipped for cgroup %q: %v",
					candidate.cgroupPath, err)
			}
		}
	}
}

func (s *beforeOOMMemsnap) captureAllowed(cfg *BeforeOOMConfig,
	now time.Time,
) bool {
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	return s.lastAttempt.IsZero() || now.Sub(s.lastAttempt) >= cooldown
}

func (s *beforeOOMMemsnap) bestCaptureCandidate(ctx context.Context,
	cfg *BeforeOOMConfig, events <-chan memoryPressureEvent,
	first memoryPressureEvent,
) (memcgCandidate, bool, error) {
	pending := map[string]memoryPressureEvent{first.cgroupPath: first}
	timer := time.NewTimer(arbitrationDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return memcgCandidate{}, false, ctx.Err()
		case event, ok := <-events:
			if !ok {
				return s.highestPressureCandidate(cfg, pending)
			}
			pending[event.cgroupPath] = event
		case <-timer.C:
			return s.highestPressureCandidate(cfg, pending)
		}
	}
}

func (s *beforeOOMMemsnap) highestPressureCandidate(cfg *BeforeOOMConfig,
	events map[string]memoryPressureEvent,
) (memcgCandidate, bool, error) {
	var selected memcgCandidate
	var lastErr error
	found := false
	for _, event := range events {
		candidate, ok, err := s.pressureCandidate(event)
		if err != nil {
			lastErr = err
			continue
		}
		if !ok || candidate.containerID == "" || isUnlimitedLimit(candidate.max) {
			continue
		}
		candidate.ratio = float64(candidate.current) / float64(candidate.max)
		if candidate.ratio < float64(cfg.ThresholdPercent)/100 {
			continue
		}
		if !found || higherPressure(candidate, selected) {
			selected = candidate
			found = true
		}
	}
	if found {
		return selected, true, nil
	}
	return memcgCandidate{}, false, lastErr
}

func higherPressure(candidate, selected memcgCandidate) bool {
	if candidate.ratio != selected.ratio {
		return candidate.ratio > selected.ratio
	}
	if candidate.current != selected.current {
		return candidate.current > selected.current
	}
	return candidate.cgroupPath < selected.cgroupPath
}

func (s *beforeOOMMemsnap) pressureCandidate(
	event memoryPressureEvent,
) (memcgCandidate, bool, error) {
	usage, err := s.cgroup.MemoryUsage(event.cgroupPath)
	if err != nil {
		return memcgCandidate{}, false, fmt.Errorf("read cgroup memory usage: %w", err)
	}
	if usage == nil {
		return memcgCandidate{}, false, nil
	}
	return memcgCandidate{
		containerID: event.containerID, cgroupPath: event.cgroupPath,
		current: usage.Usage, max: usage.MaxLimited,
	}, true, nil
}

// Dependencies are local to this event; tests can reject races without touching
// the shared tracing registry or process helpers.
type beforeOOMOps struct {
	selectVictim  func(context.Context, string, uint64) (victimCandidate, error)
	validate      func(context.Context, string, memsnap.ProcessIdentity) error
	collect       func(context.Context, int, collector.Options) (*collector.Result, error)
	save          func(*tracing.WriteRequest) error
	containerPath func(string) (string, error)
}

func (s *beforeOOMMemsnap) captureCandidate(ctx context.Context,
	cfg *BeforeOOMConfig, candidate *memcgCandidate,
) error {
	ops := s.captureOps
	if ops == nil {
		ops = &beforeOOMOps{
			selectVictim: selectVictim, validate: validateVictim,
			collect: collector.Run,
			save:    tracing.Save, containerPath: knownContainerCgroupPath,
		}
	}
	validateContainer := func() error {
		return validateContainerCgroup(candidate.containerID, candidate.cgroupPath, ops.containerPath)
	}
	if err := validateContainer(); err != nil {
		return err
	}
	selectionCtx, cancelSelection := context.WithTimeout(ctx, victimSelectionTimeout)
	victim, err := ops.selectVictim(selectionCtx, candidate.cgroupPath, candidate.max)
	cancelSelection()
	if err != nil {
		return fmt.Errorf("select victim: %w", err)
	}
	_, err = ops.collect(ctx, victim.pid, collector.Options{
		ExpectedIdentity: &victim.identity,
		TopK:             cfg.TopK,
		GoTimeout:        time.Duration(cfg.GoTimeoutMS) * time.Millisecond,
		JavaTimeout:      time.Duration(cfg.JavaTimeoutMS) * time.Millisecond,
		PythonTimeout:    time.Duration(cfg.PythonTimeoutMS) * time.Millisecond,
		CheckTarget: func(checkCtx context.Context, identity memsnap.ProcessIdentity) error {
			return ops.validate(checkCtx, candidate.cgroupPath, identity)
		},
		Save: func(saveCtx context.Context, result *collector.Result) error {
			if err := validateContainer(); err != nil {
				return err
			}
			if err := saveCtx.Err(); err != nil {
				return err
			}
			// Persistence retains the shared synchronous tracing.Save contract.
			return ops.save(&tracing.WriteRequest{
				TracerName: beforeOOMTracer, ContainerID: candidate.containerID,
				ObservedTimestamp: result.CaptureTime,
				TracerData: &beforeOOMData{
					CgroupPath:    candidate.cgroupPath,
					MemoryCurrent: candidate.current, MemoryMax: candidate.max,
					MemoryUsagePercent: candidate.ratio * 100,
					VictimPID:          victim.pid, VictimProcessName: victim.comm,
					VictimOOMScoreAdj: victim.oomScoreAdj, Language: result.Language,
					Snapshot: result.Snapshot,
				},
			})
		},
	})
	return err
}

func knownContainerCgroupPath(id string) (string, error) {
	container, err := pod.ContainerByID(id)
	if err != nil {
		return "", err
	}
	if container == nil {
		return "", fmt.Errorf("container %q is no longer known", id)
	}
	return memcgPathForPID(container.InitPid, cgroups.CgroupMode())
}

func memcgPathForPID(initPID int, mode cgroups.Mode) (string, error) {
	paths, err := cgroups.PathsForPID(initPID)
	if err != nil {
		return "", err
	}
	// CPU and memory controllers can use different parents on v1.
	path := paths.Controllers["memory"]
	if mode == cgroups.Unified {
		path = paths.Unified
	}
	if path == "" {
		return "", fmt.Errorf("container init pid %d has no memory cgroup path", initPID)
	}
	return path, nil
}

func validateContainerCgroup(id, path string, lookup func(string) (string, error)) error {
	if lookup == nil {
		return errors.New("before-OOM container path lookup is unavailable")
	}
	known, err := lookup(id)
	if err != nil {
		return err
	}
	if known == "" || !filepath.IsAbs(path) || !filepath.IsAbs(known) ||
		filepath.Clean(known) != filepath.Clean(path) {
		return fmt.Errorf("container %q does not match cgroup %q", id, path)
	}
	return nil
}

func selectVictim(ctx context.Context, cgroupPath string, memoryMax uint64) (victimCandidate, error) {
	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return victimCandidate{}, fmt.Errorf("open procfs: %w", err)
	}
	return selectVictimFromProcs(func(visit func(int) error) error {
		return scanMemcgProcs(ctx, cgroupPath, visit)
	}, func(pid int) (victimCandidate, error) {
		identity, err := memsnap.ReadIdentity(pid)
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
		current, err := memsnap.ReadIdentity(pid)
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

func validateVictim(ctx context.Context, cgroupPath string, identity memsnap.ProcessIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := memsnap.ValidateIdentity("/proc", identity); err != nil {
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
	return memsnap.ValidateIdentity("/proc", identity)
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

type watchedCgroup struct {
	containerID  string
	cgroupPath   string
	eventFD      int
	inotifyWatch int
	eventsPath   string
	eventCount   uint64
}

type memoryPressureEvent struct {
	containerID string
	cgroupPath  string
}

type pressureWatcher struct {
	cgroup    cgroups.Cgroup
	cfg       *BeforeOOMConfig
	mode      cgroups.Mode
	root      string
	epollFD   int
	inotifyFD int
	controlFD int

	cgroups          map[string]*watchedCgroup
	pressureFDs      map[int]string
	inotifyWatches   map[int]string
	directoryWatches map[int]string
	directoryWatchFD map[string]int
	limitReported    bool
}

func newPressureWatcher(cgroup cgroups.Cgroup,
	cfg *BeforeOOMConfig,
) (*pressureWatcher, error) {
	mode := cgroups.CgroupMode()
	if mode != cgroups.Legacy && mode != cgroups.Hybrid && mode != cgroups.Unified {
		return nil, fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	root, err := memoryCgroupRoot(mode)
	if err != nil {
		return nil, err
	}

	epollFD, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("create memory watcher epoll: %w", err)
	}
	inotifyFD, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		_ = unix.Close(epollFD)
		return nil, fmt.Errorf("create memory watcher inotify: %w", err)
	}
	controlFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = unix.Close(inotifyFD)
		_ = unix.Close(epollFD)
		return nil, fmt.Errorf("create memory watcher control eventfd: %w", err)
	}
	for _, fd := range []int{inotifyFD, controlFD} {
		if err := unix.EpollCtl(epollFD, unix.EPOLL_CTL_ADD, fd,
			&unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)}); err != nil {
			_ = unix.Close(controlFD)
			_ = unix.Close(inotifyFD)
			_ = unix.Close(epollFD)
			return nil, fmt.Errorf("add memory watcher fd to epoll: %w", err)
		}
	}
	return &pressureWatcher{
		cgroup: cgroup, cfg: cfg, mode: mode, root: root,
		epollFD: epollFD, inotifyFD: inotifyFD, controlFD: controlFD,
		cgroups:          make(map[string]*watchedCgroup),
		pressureFDs:      make(map[int]string),
		inotifyWatches:   make(map[int]string),
		directoryWatches: make(map[int]string),
		directoryWatchFD: make(map[string]int),
	}, nil
}

func (w *pressureWatcher) Run(ctx context.Context) (
	<-chan memoryPressureEvent, <-chan error,
) {
	events := make(chan memoryPressureEvent, 1)
	done := make(chan error, 1)
	go func() {
		runCtx, cancel := context.WithCancel(ctx)
		controlDone := make(chan struct{})
		go w.forwardCancellation(runCtx, controlDone)
		runErr := w.loop(runCtx, events)
		cancel()
		<-controlDone
		w.close()
		if errors.Is(runErr, context.Canceled) {
			runErr = nil
		}
		done <- runErr
		close(done)
		close(events)
	}()
	return events, done
}

func (w *pressureWatcher) forwardCancellation(ctx context.Context,
	done chan<- struct{},
) {
	defer close(done)
	<-ctx.Done()
	signalEventFD(w.controlFD)
}

func (w *pressureWatcher) loop(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	if err := w.refreshFromCgroupTree(ctx, events); err != nil {
		return err
	}

	epollEvents := make([]unix.EpollEvent, maxEpollEvents)
	for {
		n, err := unix.EpollWait(w.epollFD, epollEvents, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for cgroup memory event: %w", err)
		}
		for i := 0; i < n; i++ {
			fd := int(epollEvents[i].Fd)
			if fd == w.controlFD {
				if err := drainEventFD(fd); err != nil {
					return fmt.Errorf("drain memory watcher control eventfd: %w", err)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				continue
			}
			if fd == w.inotifyFD {
				if err := w.handleInotify(ctx, events); err != nil {
					return err
				}
				continue
			}
			cgroupPath, ok := w.pressureFDs[fd]
			if !ok {
				continue
			}
			if epollEvents[i].Events&(unix.EPOLLERR|unix.EPOLLHUP) != 0 {
				if err := w.recoverV1Watch(cgroupPath,
					fmt.Errorf("pressure eventfd closed (events=%#x)",
						epollEvents[i].Events)); err != nil {
					return err
				}
				continue
			}
			if err := drainEventFD(fd); err != nil {
				if recoverErr := w.recoverV1Watch(cgroupPath,
					fmt.Errorf("drain pressure eventfd: %w", err)); recoverErr != nil {
					return recoverErr
				}
				continue
			}
			if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
				return err
			}
		}
	}
}

func (w *pressureWatcher) refreshFromCgroupTree(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	desired := make(map[string]string)
	directories, err := w.walkCgroupTree(ctx, w.root, func(containerID, cgroupPath string) error {
		if previous, exists := desired[containerID]; exists && previous != cgroupPath {
			// Do not let directory enumeration order choose an identity.
			desired[containerID] = ""
		} else {
			desired[containerID] = cgroupPath
		}
		return nil
	})
	incomplete := errors.Is(err, errCgroupWatchLimit)
	if err != nil && !incomplete {
		return fmt.Errorf("discover before-OOM cgroups: %w", err)
	}
	if incomplete {
		w.reportWatchLimit()
	} else {
		w.reconcileDirectoryWatches(directories)
	}
	addedPaths, err := w.reconcile(desired, !incomplete)
	if errors.Is(err, errCgroupWatchLimit) {
		w.reportWatchLimit()
	} else if err != nil {
		return err
	}
	if w.mode == cgroups.Unified {
		return nil
	}
	for _, cgroupPath := range addedPaths {
		if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
			return err
		}
	}
	return nil
}

func (w *pressureWatcher) reportWatchLimit() {
	if !w.limitReported {
		log.Warnf("%v; additional cgroups may not be monitored", errCgroupWatchLimit)
		w.limitReported = true
	}
}

func (w *pressureWatcher) walkCgroupTree(ctx context.Context, start string,
	visitContainer func(containerID, cgroupPath string) error,
) (map[string]struct{}, error) {
	directories := make(map[string]struct{})
	if err := ctx.Err(); err != nil {
		return directories, err
	}
	info, err := os.Lstat(start)
	if err != nil {
		return directories, err
	}
	if !info.IsDir() || !pathWithin(w.root, start) {
		return directories, fmt.Errorf("invalid cgroup discovery root %q", start)
	}
	relative, err := filepath.Rel(w.root, start)
	if err != nil {
		return directories, err
	}
	depth := 0
	if relative != "." {
		depth = strings.Count(relative, string(filepath.Separator)) + 1
	}
	if depth > maxCgroupDepth {
		return directories, errCgroupWatchLimit
	}
	type pendingDirectory struct {
		path  string
		depth int
	}
	queue := []pendingDirectory{{path: start, depth: depth}}
	entriesRead, containers := 0, 0
	for index := 0; index < len(queue); index++ {
		if err := ctx.Err(); err != nil {
			return directories, err
		}
		current := queue[index]
		if id := parseContainerID(filepath.Base(current.path)); id != "" {
			containers++
			if containers > maxWatchedCgroups {
				return directories, errCgroupWatchLimit
			}
			path, err := relativeCgroupPath(w.root, current.path)
			if err != nil {
				return directories, err
			}
			if err := visitContainer(id, path); err != nil {
				return directories, err
			}
			continue
		}
		if err := w.addDirectoryWatch(current.path); err != nil {
			if errors.Is(err, os.ErrNotExist) && index != 0 {
				continue
			}
			return directories, err
		}
		directories[current.path] = struct{}{}
		// Read bounded batches, rather than WalkDir's whole-directory sort.
		fd, err := unix.Open(current.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return directories, err
		}
		directory := os.NewFile(uintptr(fd), current.path)
		scanErr := func() error {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				entries, err := directory.ReadDir(128)
				entriesRead += len(entries)
				if entriesRead > maxCgroupScanEntries {
					return errCgroupWatchLimit
				}
				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					if len(queue) >= maxCgroupDirectories || current.depth >= maxCgroupDepth {
						return errCgroupWatchLimit
					}
					queue = append(queue, pendingDirectory{
						path: filepath.Join(current.path, entry.Name()), depth: current.depth + 1,
					})
				}
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		}()
		_ = directory.Close()
		if scanErr != nil {
			return directories, scanErr
		}
	}
	return directories, ctx.Err()
}

func parseContainerID(name string) string {
	match := containerCgroupIDRegexp.FindStringSubmatch(name)
	if len(match) < 2 {
		return ""
	}
	if match[1] != "" {
		return match[1]
	}
	return match[2]
}

func relativeCgroupPath(root, fullPath string) (string, error) {
	relativePath, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(relativePath), nil
}

func (w *pressureWatcher) reconcile(desired map[string]string, complete bool) ([]string, error) {
	var addedPaths []string
	// Unseen entries are not deleted when discovery did not finish.
	if complete {
		for cgroupPath, entry := range w.cgroups {
			if desiredPath, ok := desired[entry.containerID]; !ok || desiredPath != cgroupPath {
				w.removeCgroup(cgroupPath)
			}
		}
	}
	for containerID, cgroupPath := range desired {
		if containerID == "" || cgroupPath == "" {
			continue
		}
		added, err := w.watchContainer(containerID, cgroupPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if !complete && errors.Is(err, errCgroupWatchLimit) {
				return addedPaths, err
			}
			return nil, err
		}
		if added {
			addedPaths = append(addedPaths, cgroupPath)
		}
	}
	return addedPaths, nil
}

func (w *pressureWatcher) watchContainer(containerID,
	cgroupPath string,
) (bool, error) {
	if oldPath, ok := w.cgroupPathForContainer(containerID); ok {
		if oldPath == cgroupPath {
			return false, nil
		}
		if _, err := os.Stat(w.memcgDir(oldPath)); !errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		w.removeCgroup(oldPath)
	}
	if entry, ok := w.cgroups[cgroupPath]; ok {
		entry.containerID = containerID
		return false, nil
	}
	return true, w.addCgroup(containerID, cgroupPath)
}

func (w *pressureWatcher) cgroupPathForContainer(containerID string) (string, bool) {
	for cgroupPath, entry := range w.cgroups {
		if entry.containerID == containerID {
			return cgroupPath, true
		}
	}
	return "", false
}

func (w *pressureWatcher) addCgroup(containerID, cgroupPath string) error {
	if len(w.cgroups) >= maxWatchedCgroups {
		return errCgroupWatchLimit
	}
	entry := &watchedCgroup{
		containerID: containerID, cgroupPath: cgroupPath,
		eventFD: -1, inotifyWatch: -1,
	}
	w.cgroups[cgroupPath] = entry
	var err error
	switch w.mode {
	case cgroups.Legacy, cgroups.Hybrid:
		err = w.addV1Cgroup(entry)
	case cgroups.Unified:
		err = w.addV2Cgroup(entry)
	}
	if err != nil {
		w.removeCgroup(cgroupPath)
		return fmt.Errorf("watch memory cgroup %q: %w", cgroupPath, err)
	}
	return nil
}

func (w *pressureWatcher) addV1Cgroup(entry *watchedCgroup) error {
	directory := w.memcgDir(entry.cgroupPath)
	if err := w.addInotify(entry, filepath.Join(directory,
		"memory.limit_in_bytes")); err != nil {
		return err
	}
	return w.rearmV1Threshold(entry)
}

func (w *pressureWatcher) rearmV1Threshold(entry *watchedCgroup) error {
	directory := w.memcgDir(entry.cgroupPath)
	usage, err := w.cgroup.MemoryUsage(entry.cgroupPath)
	if err != nil {
		return err
	}
	if usage == nil || isUnlimitedLimit(usage.MaxLimited) {
		w.removeEventFD(entry)
		return nil
	}
	threshold := percentOfLimit(usage.MaxLimited, w.cfg.ThresholdPercent)
	fd, err := registerV1Threshold(directory, threshold)
	if err != nil {
		return err
	}
	return w.addEventFD(entry, fd)
}

func (w *pressureWatcher) addV2Cgroup(entry *watchedCgroup) error {
	directory := w.memcgDir(entry.cgroupPath)
	eventsPath := filepath.Join(directory, "memory.events.local")
	if _, err := os.Stat(eventsPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat memory events %q: %w", eventsPath, err)
		}
		eventsPath = filepath.Join(directory, "memory.events")
	}
	// Cgroup v2 has no non-polling notification for an arbitrary
	// memory.current/memory.max ratio. Observe only memory.high pressure;
	// when memory.high is "max", the high counter does not increase and this
	// cgroup remains quiet until a finite memory.high is configured.
	// Establish the baseline with the watch already installed. Earlier events
	// are historical; increments after this read remain observable.
	if err := w.addInotify(entry, eventsPath); err != nil {
		return err
	}
	eventCount, err := readMemoryEventCounter(eventsPath, "high")
	if err != nil {
		return err // addCgroup removes the installed watch on failure.
	}
	entry.eventsPath = eventsPath
	entry.eventCount = eventCount
	return nil
}

func (w *pressureWatcher) addInotify(entry *watchedCgroup, filePath string) error {
	mask := uint32(unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_DELETE_SELF |
		unix.IN_MOVE_SELF)
	wd, err := unix.InotifyAddWatch(w.inotifyFD, filePath, mask)
	if err != nil {
		return err
	}
	entry.inotifyWatch = wd
	w.inotifyWatches[wd] = entry.cgroupPath
	return nil
}

func (w *pressureWatcher) addDirectoryWatch(directory string) error {
	oldWatch, exists := w.directoryWatchFD[directory]
	if !exists && len(w.directoryWatchFD) >= maxCgroupDirectories {
		return errCgroupWatchLimit
	}
	mask := uint32(unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM |
		unix.IN_MOVED_TO | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF)
	wd, err := unix.InotifyAddWatch(w.inotifyFD, directory, mask)
	if err != nil {
		return err
	}
	if exists && oldWatch != wd {
		delete(w.directoryWatches, oldWatch)
		_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(oldWatch))
	}
	if oldDirectory, ok := w.directoryWatches[wd]; ok {
		delete(w.directoryWatchFD, oldDirectory)
	}
	w.directoryWatches[wd] = directory
	w.directoryWatchFD[directory] = wd
	return nil
}

func (w *pressureWatcher) reconcileDirectoryWatches(desired map[string]struct{}) {
	for directory := range w.directoryWatchFD {
		if _, ok := desired[directory]; !ok {
			w.removeDirectoryWatch(directory)
		}
	}
}

func (w *pressureWatcher) removeDirectoryWatch(directory string) {
	wd, ok := w.directoryWatchFD[directory]
	if !ok {
		return
	}
	delete(w.directoryWatchFD, directory)
	delete(w.directoryWatches, wd)
	_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(wd))
}

func (w *pressureWatcher) addEventFD(entry *watchedCgroup, fd int) error {
	if err := unix.EpollCtl(w.epollFD, unix.EPOLL_CTL_ADD, fd,
		&unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)}); err != nil {
		_ = unix.Close(fd)
		return err
	}
	oldFD := entry.eventFD
	entry.eventFD = fd
	w.pressureFDs[fd] = entry.cgroupPath
	if oldFD >= 0 {
		delete(w.pressureFDs, oldFD)
		_ = unix.EpollCtl(w.epollFD, unix.EPOLL_CTL_DEL, oldFD, nil)
		_ = unix.Close(oldFD)
	}
	return nil
}

func (w *pressureWatcher) removeEventFD(entry *watchedCgroup) {
	if entry.eventFD < 0 {
		return
	}
	delete(w.pressureFDs, entry.eventFD)
	_ = unix.EpollCtl(w.epollFD, unix.EPOLL_CTL_DEL, entry.eventFD, nil)
	_ = unix.Close(entry.eventFD)
	entry.eventFD = -1
}

func (w *pressureWatcher) handleInotify(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	buffer := make([]byte, inotifyBufferBytes)
	for batches := 0; batches < 8; batches++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Read(w.inotifyFD, buffer)
		if err == unix.EAGAIN {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read cgroup memory inotify: %w", err)
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			if err := ctx.Err(); err != nil {
				return err
			}
			wd := int(int32(binary.NativeEndian.Uint32(buffer[offset : offset+4])))
			mask := binary.NativeEndian.Uint32(buffer[offset+4 : offset+8])
			nameLength := int(binary.NativeEndian.Uint32(buffer[offset+12 : offset+16]))
			nameStart := offset + unix.SizeofInotifyEvent
			nameEnd := nameStart + nameLength
			if nameEnd > n {
				return errors.New("truncated cgroup inotify event")
			}
			name := strings.TrimRight(string(buffer[nameStart:nameEnd]), "\x00")
			offset += unix.SizeofInotifyEvent + nameLength
			if mask&unix.IN_Q_OVERFLOW != 0 {
				if err := w.handleInotifyOverflow(ctx, events); err != nil {
					return err
				}
				continue
			}
			if directory, ok := w.directoryWatches[wd]; ok {
				if err := w.handleCgroupDirectoryEvent(ctx, events, directory,
					name, mask); err != nil {
					return err
				}
				continue
			}
			cgroupPath, ok := w.inotifyWatches[wd]
			if !ok {
				continue
			}
			if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_IGNORED) != 0 {
				w.removeCgroup(cgroupPath)
				continue
			}
			emit, handleErr := w.handleMemoryChange(cgroupPath)
			if handleErr != nil {
				if errors.Is(handleErr, os.ErrNotExist) {
					w.removeCgroup(cgroupPath)
					continue
				}
				if isResourceExhaustion(handleErr) {
					return handleErr
				}
				if w.mode == cgroups.Unified {
					// Keep the v2 watch and the last observed counter. A later
					// memory.events modification retries the read and catches the
					// cumulative high-counter increase.
					log.Debugf("memory pressure watch for cgroup %q retained after read error: %v",
						cgroupPath, handleErr)
					continue
				}
				if err := w.recoverV1Watch(cgroupPath, handleErr); err != nil {
					return err
				}
				continue
			}
			if emit {
				if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (w *pressureWatcher) handleCgroupDirectoryEvent(ctx context.Context,
	events chan<- memoryPressureEvent, directory, name string, mask uint32,
) error {
	if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_IGNORED) != 0 {
		if directory == w.root {
			return fmt.Errorf("memory cgroup root %q disappeared: %w",
				directory, os.ErrNotExist)
		}
		w.removeCgroupTree(directory)
		return nil
	}
	if mask&unix.IN_ISDIR == 0 || name == "" {
		return nil
	}
	target := filepath.Join(directory, name)
	if mask&(unix.IN_DELETE|unix.IN_MOVED_FROM) != 0 {
		w.removeCgroupTree(target)
	}
	if mask&(unix.IN_CREATE|unix.IN_MOVED_TO) == 0 {
		return nil
	}
	_, err := w.walkCgroupTree(ctx, target, func(containerID, cgroupPath string) error {
		added, watchErr := w.watchContainer(containerID, cgroupPath)
		if watchErr != nil {
			if errors.Is(watchErr, os.ErrNotExist) {
				return nil
			}
			return watchErr
		}
		if added && w.mode != cgroups.Unified {
			return w.emitPressure(ctx, events, cgroupPath)
		}
		return nil
	})
	if errors.Is(err, errCgroupWatchLimit) {
		w.reportWatchLimit()
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (w *pressureWatcher) removeCgroupTree(directory string) {
	for cgroupPath := range w.cgroups {
		if pathWithin(directory, w.memcgDir(cgroupPath)) {
			w.removeCgroup(cgroupPath)
		}
	}
	for watchedDirectory := range w.directoryWatchFD {
		if pathWithin(directory, watchedDirectory) {
			w.removeDirectoryWatch(watchedDirectory)
		}
	}
}

func pathWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (w *pressureWatcher) handleMemoryChange(cgroupPath string) (bool, error) {
	switch w.mode {
	case cgroups.Legacy, cgroups.Hybrid:
		entry, ok := w.cgroups[cgroupPath]
		if !ok {
			return false, nil
		}
		if err := w.rearmV1Threshold(entry); err != nil {
			return false, err
		}
		return true, nil
	case cgroups.Unified:
		return w.observeV2EventIncrease(cgroupPath)
	default:
		return false, nil
	}
}

func (w *pressureWatcher) handleInotifyOverflow(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	// Directory deletion/recreation notifications may also have been lost.
	for directory := range w.directoryWatchFD {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.addDirectoryWatch(directory); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				w.removeDirectoryWatch(directory)
				continue
			}
			return err
		}
	}
	if w.mode != cgroups.Unified {
		paths := make([]string, 0, len(w.cgroups))
		for path := range w.cgroups {
			paths = append(paths, path)
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := w.cgroups[path].containerID
			w.removeCgroup(path)
			if err := w.addCgroup(id, path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			if err := w.emitPressure(ctx, events, path); err != nil {
				return err
			}
		}
		return w.refreshFromCgroupTree(ctx, events)
	}
	for cgroupPath, entry := range w.cgroups {
		if err := ctx.Err(); err != nil {
			return err
		}
		previousWatch, previousCount := entry.inotifyWatch, entry.eventCount
		err := w.addV2Cgroup(entry)
		// Re-adding an inode already watched returns the same descriptor. A
		// replacement at the same path needs its own initial counter baseline.
		sameWatch := entry.inotifyWatch == previousWatch
		if !sameWatch {
			delete(w.inotifyWatches, previousWatch)
			_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(previousWatch))
		}
		if err != nil {
			if isResourceExhaustion(err) {
				return err
			}
			if !sameWatch || errors.Is(err, os.ErrNotExist) {
				w.removeCgroup(cgroupPath)
			} else {
				// A failed read must not erase an existing cumulative baseline.
				log.Debugf("restore v2 pressure watch for cgroup %q: %v", cgroupPath, err)
			}
			continue
		}
		if sameWatch && entry.eventCount > previousCount {
			if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
				return err
			}
		}
	}
	return w.refreshFromCgroupTree(ctx, events)
}

func (w *pressureWatcher) observeV2EventIncrease(cgroupPath string) (bool, error) {
	entry, ok := w.cgroups[cgroupPath]
	if !ok || entry.eventsPath == "" {
		return false, nil
	}
	eventCount, err := readMemoryEventCounter(entry.eventsPath, "high")
	if err != nil {
		return false, err
	}
	increased := eventCount > entry.eventCount
	entry.eventCount = eventCount
	return increased, nil
}

func (w *pressureWatcher) emitPressure(ctx context.Context,
	events chan<- memoryPressureEvent, cgroupPath string,
) error {
	entry, ok := w.cgroups[cgroupPath]
	if !ok || entry.containerID == "" {
		return nil
	}
	select {
	case events <- memoryPressureEvent{
		containerID: entry.containerID, cgroupPath: entry.cgroupPath,
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *pressureWatcher) removeCgroup(cgroupPath string) {
	entry, ok := w.cgroups[cgroupPath]
	if !ok {
		return
	}
	delete(w.cgroups, cgroupPath)
	w.removeEventFD(entry)
	if entry.inotifyWatch >= 0 {
		delete(w.inotifyWatches, entry.inotifyWatch)
		_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(entry.inotifyWatch))
	}
}

func (w *pressureWatcher) recoverV1Watch(cgroupPath string, cause error) error {
	entry, ok := w.cgroups[cgroupPath]
	if !ok {
		return nil
	}
	w.removeEventFD(entry)
	if err := w.rearmV1Threshold(entry); err == nil {
		log.Debugf("memory pressure watch for cgroup %q restored after error: %v",
			cgroupPath, cause)
		return nil
	} else {
		w.removeCgroup(cgroupPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("restore memory pressure watch for cgroup %q after %w: %w",
			cgroupPath, cause, err)
	}
}

func (w *pressureWatcher) close() {
	for cgroupPath := range w.cgroups {
		w.removeCgroup(cgroupPath)
	}
	for directory := range w.directoryWatchFD {
		w.removeDirectoryWatch(directory)
	}
	if w.inotifyFD >= 0 {
		_ = unix.Close(w.inotifyFD)
		w.inotifyFD = -1
	}
	if w.controlFD >= 0 {
		_ = unix.Close(w.controlFD)
		w.controlFD = -1
	}
	if w.epollFD >= 0 {
		_ = unix.Close(w.epollFD)
		w.epollFD = -1
	}
}

func drainEventFD(fd int) error {
	var buffer [8]byte
	for {
		_, err := unix.Read(fd, buffer[:])
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == nil {
			return nil
		}
		return err
	}
}

func signalEventFD(fd int) {
	var buffer [8]byte
	binary.NativeEndian.PutUint64(buffer[:], 1)
	for {
		_, err := unix.Write(fd, buffer[:])
		if err == unix.EINTR {
			continue
		}
		if err != nil && err != unix.EAGAIN {
			log.Debugf("signal memory watcher control eventfd: %v", err)
		}
		return
	}
}

func memoryCgroupRoot(mode cgroups.Mode) (string, error) {
	var root string
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		root = cgroups.RootFsFilePath(subsystem.SubsystemMemory)
	case cgroups.Unified:
		root = cgroups.RootfsDefaultPath()
	default:
		return "", fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve memory cgroup root %q: %w", root, err)
	}
	return realRoot, nil
}

func (w *pressureWatcher) memcgDir(cgroupPath string) string {
	cleanPath := strings.TrimPrefix(filepath.Clean("/"+cgroupPath), "/")
	return filepath.Join(w.root, cleanPath)
}

func registerV1Threshold(directory string, threshold uint64) (int, error) {
	eventFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return -1, err
	}
	usageFile, err := os.Open(filepath.Join(directory, "memory.usage_in_bytes"))
	if err != nil {
		_ = unix.Close(eventFD)
		return -1, err
	}
	defer usageFile.Close()
	eventControl, err := os.OpenFile(filepath.Join(directory, "cgroup.event_control"),
		os.O_WRONLY, 0)
	if err != nil {
		_ = unix.Close(eventFD)
		return -1, err
	}
	_, writeErr := fmt.Fprintf(eventControl, "%d %d %d", eventFD,
		usageFile.Fd(), threshold)
	closeErr := eventControl.Close()
	if writeErr != nil {
		_ = unix.Close(eventFD)
		return -1, writeErr
	}
	if closeErr != nil {
		_ = unix.Close(eventFD)
		return -1, closeErr
	}
	return eventFD, nil
}

func percentOfLimit(limit uint64, percent int) uint64 {
	return limit/100*uint64(percent) + limit%100*uint64(percent)/100
}

func isUnlimitedLimit(limit uint64) bool {
	return limit == 0 || limit == math.MaxUint64 ||
		limit >= uint64(math.MaxInt64)-(1<<20)
}

func readMemoryEventCounter(eventsPath, counter string) (uint64, error) {
	events, err := parseutil.RawKV(eventsPath)
	if err != nil {
		return 0, fmt.Errorf("read memory events %q: %w", eventsPath, err)
	}
	count, ok := events[counter]
	if !ok {
		return 0, fmt.Errorf("memory events %q has no %s counter", eventsPath, counter)
	}
	return count, nil
}
