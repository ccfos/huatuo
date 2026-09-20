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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
)

const (
	inotifyBufferBytes = 64 * 1024
	maxEpollEvents     = 64
)

func isResourceExhaustion(err error) bool {
	return errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) ||
		errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.ENOMEM)
}

func handleWatchError(ctx context.Context, err error) error {
	if !isResourceExhaustion(err) {
		return err
	}
	log.WithError(err).
		Error("before-OOM memory snapshot stopped after resource exhaustion; increase process FD/inotify limits and restart huatuo-bamai to re-enable it")
	// Keep Start blocked until shutdown so the generic event runner does not
	// repeatedly rebuild and rescan the whole cgroup tree.
	<-ctx.Done()
	return nil
}

type watchedCgroup struct {
	containerID  string
	cgroupPath   string
	eventFD      int
	inotifyWatch int
	eventsPath   string
	eventCount   uint64
	identity     os.FileInfo
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

	cgroups           map[string]*watchedCgroup
	pressureFDs       map[int]string
	inotifyWatches    map[int]string
	lifecycle         *pod.MemoryCgroupSubscription
	changes           <-chan pod.MemoryCgroupChange
	containerPath     func(string) (string, error)
	limitReported     bool
	recoveryDue       time.Time
	recoveryRequested bool
	recoveryAttempts  int
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

	w, err := openPressureWatcher(cgroup, cfg, mode, root)
	if err != nil {
		return nil, err
	}
	w.lifecycle, err = pod.SubscribeMemoryCgroups(func() { signalEventFD(w.controlFD) })
	if err != nil {
		w.close()
		return nil, fmt.Errorf("subscribe memory cgroup lifecycle: %w", err)
	}
	w.changes = w.lifecycle.Changes()
	return w, nil
}

func openPressureWatcher(cgroup cgroups.Cgroup, cfg *BeforeOOMConfig,
	mode cgroups.Mode, root string,
) (*pressureWatcher, error) {
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
		cgroups:        make(map[string]*watchedCgroup),
		pressureFDs:    make(map[int]string),
		inotifyWatches: make(map[int]string),
		containerPath:  pod.ContainerMemoryCgroupPathByID,
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
		if w.lifecycle != nil && w.lifecycle.TakeResync() {
			w.requestRecovery()
		}
		if !w.recoveryDue.IsZero() && !time.Now().Before(w.recoveryDue) {
			if err := w.recoverWatches(ctx, events); err != nil {
				return err
			}
			// Consume losses that arrived during recovery before blocking.
			continue
		}
		timeout := -1
		if !w.recoveryDue.IsZero() {
			timeout = max(0, int((time.Until(w.recoveryDue)+time.Millisecond-1)/time.Millisecond))
		}
		n, err := unix.EpollWait(w.epollFD, epollEvents, timeout)
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
				if err := w.handleCgroupChanges(ctx, events); err != nil {
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

func (w *pressureWatcher) addCgroup(containerID, cgroupPath string) error {
	if len(w.cgroups) >= maxWatchedCgroups {
		return errCgroupWatchLimit
	}
	info, err := os.Lstat(w.memcgDir(cgroupPath))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("memory cgroup %q is not a directory", cgroupPath)
	}
	entry := &watchedCgroup{
		containerID: containerID, cgroupPath: cgroupPath, identity: info,
		eventFD: -1, inotifyWatch: -1,
	}
	w.cgroups[cgroupPath] = entry
	switch w.mode {
	case cgroups.Legacy, cgroups.Hybrid:
		err = w.addV1Cgroup(entry)
	case cgroups.Unified:
		err = w.addV2Cgroup(entry)
	}
	if err == nil {
		var current os.FileInfo
		current, err = os.Lstat(w.memcgDir(cgroupPath))
		if err == nil && !os.SameFile(info, current) {
			err = os.ErrNotExist
		}
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
	// v2 requires finite memory.high; arbitrary usage ratios have no notification.
	// Install the watch before reading the baseline to retain later increments.
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
			offset += unix.SizeofInotifyEvent + nameLength
			if mask&unix.IN_Q_OVERFLOW != 0 {
				if err := w.handleInotifyOverflow(ctx, events); err != nil {
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
					log.WithField("cgroup", cgroupPath).
						WithError(handleErr).
						Debug("memory pressure watch retained after read error")
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
		return nil
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
				log.WithField("cgroup", cgroupPath).
					WithError(err).
					Debug("restore v2 pressure watch")
			}
			continue
		}
		if sameWatch && entry.eventCount > previousCount {
			if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
				return err
			}
		}
	}
	return nil
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
		log.WithField("cgroup", cgroupPath).
			WithError(cause).
			Debug("memory pressure watch restored after error")
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
	if w.lifecycle != nil {
		w.lifecycle.Close()
	}
	for cgroupPath := range w.cgroups {
		w.removeCgroup(cgroupPath)
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
			log.WithError(err).
				Debug("signal memory watcher control eventfd")
		}
		return
	}
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
