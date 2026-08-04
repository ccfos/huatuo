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
// MD member monitoring:
//
//   - /sys/block/mdX/md/dev-*/state reports changes to all kernel state tokens.
//   - degraded notifications refresh every member so simultaneous changes are
//     emitted separately.
//   - topology rebuilds report a previously known member that disappears as
//     removed.
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
	mdMemberState  = "state"
	mdMaxReadBytes = 4096
	mdStatMaxBytes = 1 << 20
)

type mdPollFunc func([]unix.PollFd, int) (int, error)

type mdWatchFile struct {
	file   *os.File
	array  string
	member string
	field  string
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
	members map[string]map[string]string
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
		members:        make(map[string]map[string]string),
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
		if err := w.handleDegradedEventLocked(target); err != nil {
			return err
		}
		for _, change := range w.refreshMemberStatesLocked(target.array) {
			w.emitChangeLocked(change)
		}
	case mdMemberState:
		return w.handleMemberEventLocked(target)
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

func (w *MDWatcher) handleMemberEventLocked(target *mdWatchFile) error {
	value, err := readOpenMDFile(target.file)
	if err != nil {
		return w.rebuildAfterErrorLocked(target, err)
	}
	state := normalizeMDMemberState(value)
	oldState := w.members[target.array][target.member]
	if state == oldState {
		return nil
	}
	w.members[target.array][target.member] = state
	w.emitChangeLocked(MDChange{
		Array:    target.array,
		Member:   target.member,
		Field:    MDFieldMemberState,
		OldState: oldState,
		NewState: state,
	})
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
	log.Warnf("md watcher: read %s/%s/%s: %v",
		target.array, target.member, target.field, err)
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
	nextMembers := make(map[string]map[string]string, len(devices))
	for _, device := range devices {
		mdDir := filepath.Join(w.sysBlockPath, device, "md")
		status := mdArrayState{}
		nextMembers[device] = make(map[string]string)

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

		memberEntries, err := os.ReadDir(mdDir)
		if err != nil {
			return fmt.Errorf("md watcher: enumerate %s members: %w", device, err)
		}
		for _, entry := range memberEntries {
			entryName := entry.Name()
			if !strings.HasPrefix(entryName, "dev-") {
				continue
			}
			member := strings.TrimPrefix(entryName, "dev-")
			if member == "" {
				continue
			}
			statePath := filepath.Join(mdDir, entryName, mdMemberState)
			if err := addMDPathWatch(
				nextFiles,
				statePath,
				device,
				member,
				mdMemberState,
				func(value string) {
					nextMembers[device][member] = normalizeMDMemberState(value)
				},
			); err != nil {
				return fmt.Errorf(
					"md watcher: read %s member %s: %w",
					device,
					member,
					err,
				)
			}
		}
		nextArrays[device] = status
	}

	var changes []MDChange
	if emitChanges {
		changes = changedMDArrayFields(w.arrays, nextArrays)
		changes = append(changes, changedMDMembers(w.members, nextMembers)...)
	}

	oldFiles := w.files
	w.files = nextFiles
	w.arrays = nextArrays
	w.members = nextMembers
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

func changedMDMembers(
	previous, current map[string]map[string]string,
) []MDChange {
	arrays := make([]string, 0, len(current))
	for array := range current {
		arrays = append(arrays, array)
	}
	sort.Strings(arrays)

	var changes []MDChange
	for _, array := range arrays {
		oldMembers, ok := previous[array]
		if !ok {
			continue
		}
		members := make([]string, 0, len(oldMembers))
		for member := range oldMembers {
			members = append(members, member)
		}
		sort.Strings(members)
		for _, member := range members {
			oldState := oldMembers[member]
			newState, exists := current[array][member]
			if !exists {
				newState = MDMemberStateRemoved
			}
			if oldState == newState {
				continue
			}
			changes = append(changes, MDChange{
				Array:    array,
				Member:   member,
				Field:    MDFieldMemberState,
				OldState: oldState,
				NewState: newState,
			})
		}
	}
	return changes
}

func (w *MDWatcher) refreshMemberStatesLocked(array string) []MDChange {
	targets := make([]*mdWatchFile, 0)
	for _, target := range w.files {
		if target.array == array && target.field == mdMemberState {
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].member < targets[j].member
	})

	var changes []MDChange
	for _, target := range targets {
		value, err := readOpenMDFile(target.file)
		if err != nil {
			log.Warnf(
				"md watcher: %s member %s state: %v",
				array,
				target.member,
				err,
			)
			continue
		}
		state := normalizeMDMemberState(value)
		oldState := w.members[array][target.member]
		if state == oldState {
			continue
		}
		w.members[array][target.member] = state
		changes = append(changes, MDChange{
			Array:    array,
			Member:   target.member,
			Field:    MDFieldMemberState,
			OldState: oldState,
			NewState: state,
		})
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
	return addMDPathWatch(
		files,
		filepath.Join(mdDir, field),
		array,
		"",
		field,
		consume,
	)
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
	path, array, member, field string,
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
		file:   file,
		array:  array,
		member: member,
		field:  field,
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

func normalizeMDMemberState(value string) string {
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	sort.Strings(fields)
	return strings.Join(fields, ",")
}
