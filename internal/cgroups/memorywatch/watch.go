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

package memorywatch

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

const (
	controlToken = 1
	inotifyToken = 2
	eventBatch   = 64
)

type target struct {
	id             TargetID
	path           string
	identity       os.FileInfo
	eventFD        int
	eventToken     uint64
	limitWatch     int
	eventsWatch    int
	eventsPath     string
	highCount      uint64
	maxCount       uint64
	pendingEvent   Event
	previous, next *target
}

type commandResult struct {
	id  TargetID
	err error
}

type command struct {
	run    func() (TargetID, error)
	result chan commandResult
}

// Watcher owns one event loop for all registered memory cgroups.
// Add, Remove and Close may run concurrently. ReadEvents has one consumer.
// Idle watchers do not sample memory. v1 registers byte thresholds; v2 observes
// existing high/max events and cannot notify at arbitrary usage percentages.
// No memory limit, including memory.high, is changed.
type Watcher struct {
	options                       Options
	mode                          cgroups.Mode
	root                          string
	readUsage                     func(string) (*stats.MemoryUsage, error)
	epollFD, inotifyFD, controlFD int
	nextID                        TargetID
	nextToken                     uint64
	targets                       map[TargetID]*target
	paths                         map[string]*target
	pressureTokens                map[uint64]*target
	inotifyWatches                map[int]fileWatch
	dirty                         map[TargetID]changeFlags
	commands                      chan command
	done                          chan struct{}
	ready                         chan struct{}

	// This lock also prevents a control write from racing FD closure and reuse.
	mu         sync.Mutex
	isClosing  bool
	runErr     error
	pending    map[TargetID]*target
	head, tail *target
}

// New starts monitoring and transfers resource ownership to the caller.
// The caller must Close the watcher even when no targets were registered.
func New(opts Options) (*Watcher, error) {
	root, err := cgroups.MemoryRoot()
	if err != nil {
		return nil, err
	}
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}
	return openWatcher(opts, cgroups.CgroupMode(), root, cgroup.MemoryUsage)
}

func openWatcher(opts Options, mode cgroups.Mode, root string,
	readUsage func(string) (*stats.MemoryUsage, error),
) (*Watcher, error) {
	if opts.ThresholdPercent < 1 || opts.ThresholdPercent > 100 {
		return nil, fmt.Errorf("memory threshold percent must be between 1 and 100: %d", opts.ThresholdPercent)
	}
	if opts.MaxCgroups == 0 {
		opts.MaxCgroups = 4096
	}
	if opts.MaxCgroups < 1 {
		return nil, fmt.Errorf("memory target limit must be positive: %d", opts.MaxCgroups)
	}
	if mode != cgroups.Legacy && mode != cgroups.Hybrid && mode != cgroups.Unified {
		return nil, fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	w := &Watcher{
		options: opts, mode: mode, root: root, readUsage: readUsage,
		epollFD: -1, inotifyFD: -1, controlFD: -1, nextToken: inotifyToken,
		targets:        make(map[TargetID]*target),
		paths:          make(map[string]*target),
		pressureTokens: make(map[uint64]*target),
		inotifyWatches: make(map[int]fileWatch),
		dirty:          make(map[TargetID]changeFlags),
		pending:        make(map[TargetID]*target),
		commands:       make(chan command, 1),
		done:           make(chan struct{}), ready: make(chan struct{}, 1),
	}
	var err error
	w.epollFD, err = unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err == nil {
		w.inotifyFD, err = unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	}
	if err == nil {
		w.controlFD, err = unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	}
	if err == nil {
		err = w.addEpoll(w.inotifyFD, inotifyToken)
	}
	if err == nil {
		err = w.addEpoll(w.controlFD, controlToken)
	}
	if err != nil {
		w.release()
		return nil, fmt.Errorf("initialize memory watcher: %w", err)
	}
	go w.run()
	return w, nil
}

// Add registers a hierarchy-relative absolute path, such as /kubepods/container.
// Re-adding the same live target is idempotent. A replacement receives a new ID.
// Cancellation after submission waits for completion or rollback: an error never
// leaves a new registration owned by an unknown caller.
func (w *Watcher) Add(ctx context.Context, path string) (TargetID, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return 0, fmt.Errorf("memory cgroup path must be absolute and clean: %q", path)
	}
	return w.submit(ctx, func() (TargetID, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entry, initial, err := w.addTarget(path)
		if err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			if initial != nil {
				w.removeTarget(entry)
			}
			return 0, err
		}
		if initial != nil && initial.Kind != EventUnknown {
			if err := w.publish(entry, initial); err != nil {
				w.removeTarget(entry)
				return 0, err
			}
		}
		return entry.id, nil
	})
}

// Remove unregisters an ID and discards its unread notifications. It is idempotent.
// An already returned event cannot be revoked; consumers must revalidate it.
func (w *Watcher) Remove(ctx context.Context, id TargetID) error {
	_, err := w.submit(ctx, func() (TargetID, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if entry := w.targets[id]; entry != nil {
			w.removeTarget(entry)
		}
		w.mu.Lock()
		if entry := w.pending[id]; entry != nil {
			w.removePending(entry)
		}
		w.mu.Unlock()
		return 0, nil
	})
	return err
}

func (w *Watcher) submit(ctx context.Context, run func() (TargetID, error)) (TargetID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	w.mu.Lock()
	if w.isClosing {
		err := w.closedError()
		w.mu.Unlock()
		return 0, err
	}
	w.mu.Unlock()
	command := command{run: run, result: make(chan commandResult, 1)}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.done:
		return 0, w.terminalError()
	case w.commands <- command:
	}
	w.mu.Lock()
	if !w.isClosing {
		signalEventFD(w.controlFD)
	}
	w.mu.Unlock()
	// Once submitted, acknowledgement closes the cancellation/registration race.
	select {
	case result := <-command.result:
		return result.id, result.err
	case <-w.done:
		return 0, w.terminalError()
	}
}

// ReadEvents waits for at least one observation and fills dst without retaining it.
// dst must be nonempty. Canceling ctx only ends this read, not the watcher.
func (w *Watcher) ReadEvents(ctx context.Context, dst []Event) (int, error) {
	if len(dst) == 0 {
		return 0, errors.New("memory event destination must not be empty")
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		w.mu.Lock()
		if w.isClosing {
			err := w.closedError()
			w.mu.Unlock()
			return 0, err
		}
		n := 0
		for n < len(dst) && w.head != nil {
			entry := w.head
			dst[n] = entry.pendingEvent
			w.removePending(entry)
			n++
		}
		w.mu.Unlock()
		if n != 0 {
			return n, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-w.done:
			return 0, w.terminalError()
		case <-w.ready:
		}
	}
}

// Close rejects new operations and waits for the event loop and FD cleanup.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if !w.isClosing {
		w.isClosing = true
		signalEventFD(w.controlFD)
	}
	w.mu.Unlock()
	<-w.done
	return w.runErr
}

func (w *Watcher) terminalError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closedError()
}

func (w *Watcher) closedError() error {
	if w.runErr != nil {
		return w.runErr
	}
	return ErrClosed
}

func (w *Watcher) publish(entry *target, event *Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.isClosing {
		return ErrClosed
	}
	if _, ok := w.pending[entry.id]; !ok {
		if len(w.pending) >= w.options.MaxCgroups {
			return ErrEventOverflow
		}
		w.pending[entry.id] = entry
		entry.previous = w.tail
		if w.tail == nil {
			w.head = entry
		} else {
			w.tail.next = entry
		}
		w.tail = entry
	}
	entry.pendingEvent = *event
	select {
	case w.ready <- struct{}{}:
	default:
	}
	return nil
}

func (w *Watcher) removePending(entry *target) {
	if entry.previous == nil {
		w.head = entry.next
	} else {
		entry.previous.next = entry.next
	}
	if entry.next == nil {
		w.tail = entry.previous
	} else {
		entry.next.previous = entry.previous
	}
	delete(w.pending, entry.id)
	entry.previous, entry.next = nil, nil
	entry.pendingEvent = Event{}
}

func (w *Watcher) run() {
	err := w.loop()
	if errors.Is(err, ErrClosed) {
		err = nil
	}
	w.mu.Lock()
	w.isClosing, w.runErr = true, err
	w.mu.Unlock()
	w.release()
	close(w.done)
}

func (w *Watcher) loop() error {
	var events [eventBatch]unix.EpollEvent
	buffer := make([]byte, 64*1024)
	for {
		w.mu.Lock()
		isClosing := w.isClosing
		w.mu.Unlock()
		if isClosing {
			return nil
		}
		// Bound command work so registration churn cannot starve kernel events.
		for i := 0; i < eventBatch; i++ {
			select {
			case command := <-w.commands:
				w.mu.Lock()
				isClosing := w.isClosing
				w.mu.Unlock()
				if isClosing {
					command.result <- commandResult{err: ErrClosed}
					return nil
				}
				id, err := command.run()
				command.result <- commandResult{id: id, err: err}
				if errors.Is(err, ErrEventOverflow) {
					return err
				}
			default:
				i = eventBatch
			}
		}
		timeout := -1
		if len(w.commands) != 0 {
			timeout = 0
		}
		n, err := unix.EpollWait(w.epollFD, events[:], timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for memory events: %w", err)
		}
		for i := 0; i < n; i++ {
			token := uint64(uint32(events[i].Fd)) | uint64(uint32(events[i].Pad))<<32
			switch token {
			case controlToken:
				if err := drainEventFD(w.controlFD); err != nil {
					return err
				}
			case inotifyToken:
				if err := w.readInotify(buffer); err != nil {
					return err
				}
			default:
				if entry := w.pressureTokens[token]; entry != nil {
					if err := drainEventFD(entry.eventFD); err != nil {
						if failure := w.targetError(entry, err); failure != nil {
							return failure
						}
						continue
					}
					w.dirty[entry.id] |= usageChanged
					if events[i].Events&(unix.EPOLLERR|unix.EPOLLHUP) != 0 {
						w.dirty[entry.id] |= limitChanged
					}
				}
			}
		}
		for id, change := range w.dirty {
			if entry := w.targets[id]; entry != nil {
				if err := w.observe(entry, change); err != nil {
					if failure := w.targetError(entry, err); failure != nil {
						return failure
					}
				}
			}
			delete(w.dirty, id)
		}
	}
}

func (w *Watcher) addEpoll(fd int, token uint64) error {
	return unix.EpollCtl(w.epollFD, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
		Events: unix.EPOLLIN, Fd: int32(token), Pad: int32(token >> 32),
	})
}

func (w *Watcher) addTarget(path string) (*target, *Event, error) {
	directory := filepath.Join(w.root, path)
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("memory cgroup %q is not a directory", path)
	}
	if old := w.paths[path]; old != nil {
		if os.SameFile(old.identity, info) {
			return old, nil, nil
		}
		if err := w.retireTarget(old); err != nil {
			return nil, nil, err
		}
	}
	if len(w.targets) >= w.options.MaxCgroups {
		return nil, nil, ErrTargetLimit
	}
	w.nextID++
	entry := &target{
		id: w.nextID, path: path, identity: info,
		eventFD: -1, eventsWatch: -1, limitWatch: -1,
	}
	w.targets[entry.id], w.paths[path] = entry, entry
	if w.mode == cgroups.Unified {
		err = w.addV2Target(entry)
	} else {
		err = w.addLimitWatch(entry, "memory.limit_in_bytes")
	}
	var usage *stats.MemoryUsage
	if err == nil {
		usage, err = w.readUsage(path)
	}
	if err == nil && w.mode != cgroups.Unified {
		err = w.rearmV1(entry, usage)
		if err == nil {
			// Registration initializes the kernel crossing state. Recheck usage
			// to cover a rise between the first read and registration.
			usage, err = w.readUsage(path)
		}
	}
	if err == nil {
		var current os.FileInfo
		current, err = os.Lstat(directory)
		if err == nil && !os.SameFile(info, current) {
			err = os.ErrNotExist
		}
	}
	if err != nil {
		w.removeTarget(entry)
		return nil, nil, fmt.Errorf("watch memory cgroup %q: %w", path, err)
	}
	event := w.observation(entry, usage)
	return entry, &event, nil
}

func (w *Watcher) observation(entry *target, usage *stats.MemoryUsage) Event {
	event := Event{TargetID: entry.id, ObservedAt: time.Now()}
	if usage == nil {
		event.Kind, event.Err = TargetUnavailable, errors.New("memory usage is unavailable")
		return event
	}
	event.UsageBytes, event.LimitBytes = usage.Usage, usage.MaxLimited
	if cgroups.IsMemoryLimitUnlimited(usage.MaxLimited) {
		event.Kind, event.Err = TargetUnavailable, ErrLimitUnavailable
	} else if usage.Usage >= thresholdBytes(usage.MaxLimited, w.options.ThresholdPercent) {
		event.Kind = ThresholdObserved
	}
	return event
}

func (w *Watcher) removeTarget(entry *target) {
	w.removeV1FD(entry)
	for _, wd := range []int{entry.limitWatch, entry.eventsWatch} {
		if wd >= 0 {
			delete(w.inotifyWatches, wd)
			_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(wd))
		}
	}
	delete(w.targets, entry.id)
	delete(w.paths, entry.path)
	delete(w.dirty, entry.id)
	w.mu.Lock()
	if w.pending[entry.id] != nil {
		w.removePending(entry)
	}
	w.mu.Unlock()
}

func (w *Watcher) retireTarget(entry *target) error {
	w.removeTarget(entry)
	return w.publish(entry, &Event{
		TargetID: entry.id, Kind: TargetRemoved, ObservedAt: time.Now(),
	})
}

func (w *Watcher) targetError(entry *target, cause error) error {
	if errors.Is(cause, ErrEventOverflow) || errors.Is(cause, ErrClosed) {
		return cause
	}
	if errors.Is(cause, os.ErrNotExist) {
		return w.retireTarget(entry)
	}
	return w.publish(entry, &Event{
		TargetID: entry.id, Kind: TargetUnavailable,
		ObservedAt: time.Now(), Err: cause,
	})
}

func (w *Watcher) release() {
	for _, entry := range w.targets {
		w.removeTarget(entry)
	}
	for _, fd := range []int{w.inotifyFD, w.epollFD} {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
	w.mu.Lock()
	if w.controlFD >= 0 {
		_ = unix.Close(w.controlFD)
		w.controlFD = -1
	}
	clear(w.pending)
	w.head, w.tail = nil, nil
	w.mu.Unlock()
}

func drainEventFD(fd int) error {
	var buffer [8]byte
	for {
		_, err := unix.Read(fd, buffer[:])
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN {
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
		if err != unix.EINTR {
			return
		}
	}
}
