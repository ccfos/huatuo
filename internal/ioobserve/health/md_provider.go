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

// MD array monitoring:
//
//   - /proc/mdstat discovers active arrays and notifies topology changes.
//   - /sys/block/mdX/md/level notifies layout changes and rebuilds the watches.
//   - /sys/block/mdX/md/sync_action reports idle, resync, recover, check,
//     repair, reshape, frozen, or unknown transitions.
//   - /sys/block/mdX/md/degraded reports unavailable-member count changes.
//
// The files are opened once and waited on with poll notifications. Initial
// values establish a baseline; only later transitions are emitted. No periodic
// polling or external mdadm command is used.

package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/log"
)

const (
	mdStatField    = "mdstat"
	mdLevel        = "level"
	mdSyncAction   = "sync_action"
	mdDegraded     = "degraded"
	mdMaxReadBytes = 4096
	mdStatMaxBytes = 1 << 20
)

type mdPollFunc func([]unix.PollFd, int) (int, error)

type mdWatchFile struct {
	file  *os.File
	array string
	field string
}

type mdArrayState struct {
	syncAction string
	degraded   string
}

// MDWatcher reports MD state transitions using procfs and sysfs
// notifications. It does not poll or execute external commands.
type MDWatcher struct {
	procMDStatPath string
	sysBlockPath   string
	poll           mdPollFunc
	now            func() time.Time
	ctx            context.Context

	mu      sync.RWMutex
	arrays  map[string]mdArrayState
	files   map[int32]*mdWatchFile
	changes chan MDChange

	lifecycleMu sync.Mutex
	started     bool
	done        chan struct{}
	wakeRead    int
	runErr      error
}

// NewMDWatcher creates the single MD notification watcher.
func NewMDWatcher(procMDStatPath, sysBlockPath string) *MDWatcher {
	return newMDWatcher(procMDStatPath, sysBlockPath, unix.Poll)
}

func newMDWatcher(procMDStatPath, sysBlockPath string, poll mdPollFunc) *MDWatcher {
	return &MDWatcher{
		procMDStatPath: procMDStatPath,
		sysBlockPath:   sysBlockPath,
		poll:           poll,
		now:            time.Now,
		arrays:         make(map[string]mdArrayState),
		files:          make(map[int32]*mdWatchFile),
		changes:        make(chan MDChange, 64),
		wakeRead:       -1,
	}
}

// Start reads the initial MD topology once, establishes notification watches,
// and starts the event loop. A watcher is started at most once.
func (w *MDWatcher) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.started {
		return fmt.Errorf("md watcher already started")
	}

	wake := []int{-1, -1}
	if err := unix.Pipe2(wake, unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		return fmt.Errorf("md watcher: create wake pipe: %w", err)
	}

	w.mu.Lock()
	w.ctx = ctx
	err := w.rebuildLocked(false)
	w.mu.Unlock()
	if err != nil {
		unix.Close(wake[0])
		unix.Close(wake[1])
		return err
	}

	w.started = true
	w.done = make(chan struct{})
	w.wakeRead = wake[0]
	go w.run(ctx, wake[0], wake[1], w.done)
	return nil
}

// Wait waits for the active watcher, if any, to stop and reports a fatal poll
// error. Context cancellation is a normal stop and returns nil.
func (w *MDWatcher) Wait() error {
	w.lifecycleMu.Lock()
	done := w.done
	w.lifecycleMu.Unlock()
	if done != nil {
		<-done
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	return w.runErr
}

// Changes returns state transitions observed after the initial baseline.
func (w *MDWatcher) Changes() <-chan MDChange {
	return w.changes
}

func (w *MDWatcher) run(ctx context.Context, wakeRead, wakeWrite int, done chan struct{}) {
	wakerDone := make(chan struct{})
	stopWaker := make(chan struct{})
	go func() {
		defer close(wakerDone)
		select {
		case <-ctx.Done():
			_, _ = unix.Write(wakeWrite, []byte{1})
		case <-stopWaker:
		}
	}()

	runErr := w.pollLoop(wakeRead)
	if runErr != nil {
		log.Warnf("md watcher stopped after poll error: %v", runErr)
	}
	close(stopWaker)
	<-wakerDone
	unix.Close(wakeRead)
	unix.Close(wakeWrite)

	w.mu.Lock()
	w.closeFilesLocked()
	w.mu.Unlock()

	w.lifecycleMu.Lock()
	w.wakeRead = -1
	w.runErr = runErr
	close(done)
	w.lifecycleMu.Unlock()
}

func (w *MDWatcher) pollLoop(wakeRead int) error {
	for {
		pollFDs, targets := w.pollFDs(wakeRead)
		_, err := w.poll(pollFDs, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll MD files: %w", err)
		}
		if pollFDs[0].Revents != 0 {
			return nil
		}
		for i := 1; i < len(pollFDs); i++ {
			if pollFDs[i].Revents != 0 {
				if err := w.handleEvent(targets[i], pollFDs[i].Revents); err != nil {
					return err
				}
			}
		}
	}
}

func (w *MDWatcher) pollFDs(wakeRead int) ([]unix.PollFd, []*mdWatchFile) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	pollFDs := make([]unix.PollFd, 1, len(w.files)+1)
	targets := make([]*mdWatchFile, 1, len(w.files)+1)
	pollFDs[0] = unix.PollFd{Fd: int32(wakeRead), Events: unix.POLLIN}
	for fd, target := range w.files {
		// mdstat_poll always advertises POLLIN|POLLRDNORM, so requesting
		// either would make this loop spin. MD changes add POLLPRI|POLLERR;
		// POLLERR is reported even when it isn't in the requested mask.
		// The watched sysfs attributes likewise signal changes with POLLPRI.
		pollFDs = append(pollFDs, unix.PollFd{Fd: fd, Events: unix.POLLPRI})
		targets = append(targets, target)
	}
	return pollFDs, targets
}

func (w *MDWatcher) handleEvent(target *mdWatchFile, events int16) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	fd := int32(target.file.Fd())
	if w.files[fd] != target {
		return nil
	}
	if events&(unix.POLLHUP|unix.POLLNVAL) != 0 ||
		events&unix.POLLERR != 0 && events&unix.POLLPRI == 0 {
		return w.rebuildAfterErrorLocked(
			target,
			fmt.Errorf("poll events %#x", events),
		)
	}

	switch target.field {
	case mdStatField, mdLevel:
		if err := w.rebuildLocked(true); err != nil {
			return fmt.Errorf(
				"md watcher: rebuild after %s notification: %w",
				target.field,
				err,
			)
		}
	case mdSyncAction:
		return w.handleSyncActionEventLocked(target)
	case mdDegraded:
		return w.handleDegradedEventLocked(target)
	}
	return nil
}

func (w *MDWatcher) handleSyncActionEventLocked(target *mdWatchFile) error {
	value, err := readOpenMDFile(target.file)
	if err != nil {
		return w.rebuildAfterErrorLocked(target, err)
	}

	status, ok := w.arrays[target.array]
	if !ok {
		return nil
	}
	action := normalizeMDSyncAction(value)
	if action == status.syncAction {
		return nil
	}
	change := MDChange{
		Array:    target.array,
		Field:    MDFieldSyncAction,
		OldState: status.syncAction,
		NewState: action,
	}
	status.syncAction = action
	w.arrays[target.array] = status
	w.emitChangeLocked(change)
	return nil
}

func (w *MDWatcher) handleDegradedEventLocked(target *mdWatchFile) error {
	value, err := readOpenMDFile(target.file)
	if err != nil {
		return w.rebuildAfterErrorLocked(target, err)
	}
	degraded, err := parseMDDegraded(value)
	if err != nil {
		log.Warnf("md watcher: %s degraded: %v", target.array, err)
		return nil
	}

	status, ok := w.arrays[target.array]
	if !ok {
		return nil
	}
	value = strconv.Itoa(degraded)
	if value == status.degraded {
		return nil
	}
	change := MDChange{
		Array:    target.array,
		Field:    MDFieldDegraded,
		OldState: status.degraded,
		NewState: value,
	}
	status.degraded = value
	w.arrays[target.array] = status
	w.emitChangeLocked(change)
	return nil
}

func (w *MDWatcher) rebuildAfterErrorLocked(target *mdWatchFile, err error) error {
	log.Warnf("md watcher: read %s/%s: %v", target.array, target.field, err)
	if rebuildErr := w.rebuildLocked(true); rebuildErr != nil {
		return fmt.Errorf("md watcher: rebuild watches: %w", rebuildErr)
	}
	return nil
}

func (w *MDWatcher) rebuildLocked(emitChanges bool) error {
	nextFiles := make(map[int32]*mdWatchFile)
	committed := false
	defer func() {
		if !committed {
			closeMDWatchFiles(nextFiles)
		}
	}()

	mdstat, data, err := openMDStatWatchFile(w.procMDStatPath)
	if err != nil {
		return fmt.Errorf("md watcher: open %s: %w", w.procMDStatPath, err)
	}
	nextFiles[int32(mdstat.Fd())] = &mdWatchFile{file: mdstat, field: mdStatField}

	active := parseActiveMDStatDevices(data)
	devices := make([]string, 0, len(active))
	for device := range active {
		devices = append(devices, device)
	}
	sort.Strings(devices)

	nextArrays := make(map[string]mdArrayState, len(devices))
	for _, device := range devices {
		mdDir := filepath.Join(w.sysBlockPath, device, "md")
		status := mdArrayState{}

		if err := addMDAttributeWatch(nextFiles, mdDir, device, mdLevel, nil); err != nil {
			return fmt.Errorf("md watcher: read %s level: %w", device, err)
		}
		if err := addOptionalMDAttributeWatch(nextFiles, mdDir, device, mdSyncAction,
			func(value string) {
				status.syncAction = normalizeMDSyncAction(value)
			}); err != nil {
			return fmt.Errorf("md watcher: read %s sync_action: %w", device, err)
		}
		var degradedErr error
		if err := addOptionalMDAttributeWatch(nextFiles, mdDir, device, mdDegraded,
			func(value string) {
				degraded, err := parseMDDegraded(value)
				if err != nil {
					degradedErr = err
					return
				}
				status.degraded = strconv.Itoa(degraded)
			}); err != nil {
			return fmt.Errorf("md watcher: read %s degraded: %w", device, err)
		}
		if degradedErr != nil {
			return fmt.Errorf("md watcher: parse %s degraded: %w", device, degradedErr)
		}
		nextArrays[device] = status
	}

	var changes []MDChange
	if emitChanges {
		changes = changedMDArrayFields(w.arrays, nextArrays)
	}

	oldFiles := w.files
	w.files = nextFiles
	w.arrays = nextArrays
	committed = true
	for _, change := range changes {
		w.emitChangeLocked(change)
	}
	closeMDWatchFiles(oldFiles)
	return nil
}

func changedMDArrayFields(
	previous, current map[string]mdArrayState,
) []MDChange {
	arrays := make([]string, 0, len(current))
	for array := range current {
		arrays = append(arrays, array)
	}
	sort.Strings(arrays)

	var changes []MDChange
	for _, array := range arrays {
		oldState, ok := previous[array]
		if !ok {
			continue
		}
		newState := current[array]
		if oldState.syncAction != "" && newState.syncAction != "" &&
			oldState.syncAction != newState.syncAction {
			changes = append(changes, MDChange{
				Array:    array,
				Field:    MDFieldSyncAction,
				OldState: oldState.syncAction,
				NewState: newState.syncAction,
			})
		}
		if oldState.degraded != "" && newState.degraded != "" &&
			oldState.degraded != newState.degraded {
			changes = append(changes, MDChange{
				Array:    array,
				Field:    MDFieldDegraded,
				OldState: oldState.degraded,
				NewState: newState.degraded,
			})
		}
	}
	return changes
}

func (w *MDWatcher) emitChangeLocked(change MDChange) {
	change.ObservedAt = w.now()
	select {
	case w.changes <- change:
	case <-w.ctx.Done():
	}
}

func (w *MDWatcher) closeFilesLocked() {
	closeMDWatchFiles(w.files)
	clear(w.files)
}

func closeMDWatchFiles(files map[int32]*mdWatchFile) {
	for _, target := range files {
		_ = target.file.Close()
	}
}

func addMDAttributeWatch(
	files map[int32]*mdWatchFile,
	mdDir, array, field string,
	consume func(string),
) error {
	return addMDPathWatch(files, filepath.Join(mdDir, field), array, field, consume)
}

func addOptionalMDAttributeWatch(
	files map[int32]*mdWatchFile,
	mdDir, array, field string,
	consume func(string),
) error {
	err := addMDAttributeWatch(files, mdDir, array, field, consume)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func addMDPathWatch(
	files map[int32]*mdWatchFile,
	path, array, field string,
	consume func(string),
) error {
	file, value, err := openMDWatchFile(path)
	if err != nil {
		return err
	}
	if consume != nil {
		consume(value)
	}
	files[int32(file.Fd())] = &mdWatchFile{
		file:  file,
		array: array,
		field: field,
	}
	return nil
}

func openMDWatchFile(path string) (*os.File, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	value, err := readOpenMDFileLimit(file, mdMaxReadBytes)
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return file, value, nil
}

func openMDStatWatchFile(path string) (*os.File, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	value, err := readOpenMDFileLimit(file, mdStatMaxBytes)
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return file, value, nil
}

func readOpenMDFile(file *os.File) (string, error) {
	return readOpenMDFileLimit(file, mdMaxReadBytes)
}

func readOpenMDFileLimit(file *os.File, limit int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func parseActiveMDStatDevices(data string) map[string]bool {
	devices := make(map[string]bool)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == ":" && fields[2] == "active" &&
			strings.HasPrefix(fields[0], "md") {
			devices[fields[0]] = true
		}
	}
	return devices
}

func parseMDDegraded(value string) (int, error) {
	degraded, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || degraded < 0 {
		if err == nil {
			err = fmt.Errorf("negative value %d", degraded)
		}
		return 0, err
	}
	return degraded, nil
}

func normalizeMDSyncAction(value string) string {
	action := strings.ToLower(strings.TrimSpace(value))
	switch action {
	case "idle", "resync", "recover", "check", "repair", "reshape", "frozen":
		return action
	default:
		return "unknown"
	}
}
