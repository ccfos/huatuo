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
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

func (w *Watcher) rearmV1(entry *target, usage *stats.MemoryUsage) error {
	if usage == nil || cgroups.IsMemoryLimitUnlimited(usage.MaxLimited) {
		w.removeV1FD(entry)
		return nil
	}
	threshold := thresholdBytes(usage.MaxLimited, w.options.ThresholdPercent)
	fd, err := registerThreshold(filepath.Join(w.root, entry.path), threshold)
	if err != nil {
		return err
	}
	// Epoll data identifies the registration, not a reusable numeric FD.
	w.nextToken++
	if err := w.addEpoll(fd, w.nextToken); err != nil {
		_ = unix.Close(fd)
		return err
	}
	w.removeV1FD(entry)
	entry.eventFD, entry.eventToken = fd, w.nextToken
	w.pressureTokens[entry.eventToken] = entry
	return nil
}

func (w *Watcher) removeV1FD(entry *target) {
	if entry.eventFD < 0 {
		return
	}
	delete(w.pressureTokens, entry.eventToken)
	_ = unix.EpollCtl(w.epollFD, unix.EPOLL_CTL_DEL, entry.eventFD, nil)
	_ = unix.Close(entry.eventFD)
	entry.eventFD = -1
}

func registerThreshold(directory string, threshold uint64) (int, error) {
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
	control, err := os.OpenFile(filepath.Join(directory, "cgroup.event_control"), os.O_WRONLY, 0)
	if err != nil {
		_ = unix.Close(eventFD)
		return -1, err
	}
	_, writeErr := fmt.Fprintf(control, "%d %d %d", eventFD, usageFile.Fd(), threshold)
	closeErr := control.Close()
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
