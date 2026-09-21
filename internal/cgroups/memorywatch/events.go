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
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
)

type changeFlags uint8

const (
	usageChanged changeFlags = 1 << iota
	limitChanged
	eventsChanged
	watchLost
)

type fileKind uint8

const (
	limitFile fileKind = iota + 1
	eventsFile
)

type fileWatch struct {
	target *target
	kind   fileKind
}

func (w *Watcher) addInotify(entry *target, path string, kind fileKind) (int, error) {
	mask := uint32(unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF)
	wd, err := unix.InotifyAddWatch(w.inotifyFD, path, mask)
	if err != nil {
		return -1, err
	}
	w.inotifyWatches[wd] = fileWatch{target: entry, kind: kind}
	return wd, nil
}

func (w *Watcher) removeInotify(wd int) {
	if wd >= 0 {
		delete(w.inotifyWatches, wd)
		_, _ = unix.InotifyRmWatch(w.inotifyFD, uint32(wd))
	}
}

func (w *Watcher) addLimitWatch(entry *target, name string) error {
	wd, err := w.addInotify(entry, filepath.Join(w.root, entry.path, name), limitFile)
	if err != nil {
		return err
	}
	oldWatch := entry.limitWatch
	entry.limitWatch = wd
	if oldWatch != wd {
		w.removeInotify(oldWatch)
	}
	return nil
}

func (w *Watcher) readInotify(buffer []byte) error {
	for batch := 0; batch < 8; batch++ {
		n, err := unix.Read(w.inotifyFD, buffer)
		if err == unix.EAGAIN {
			return nil
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			wd := int(int32(binary.NativeEndian.Uint32(buffer[offset : offset+4])))
			mask := binary.NativeEndian.Uint32(buffer[offset+4 : offset+8])
			length := int(binary.NativeEndian.Uint32(buffer[offset+12 : offset+16]))
			offset += unix.SizeofInotifyEvent + length
			if offset > n {
				return errors.New("truncated memory inotify event")
			}
			if mask&unix.IN_Q_OVERFLOW != 0 {
				// A loss triggers one reconciliation, not periodic memory sampling.
				for id := range w.targets {
					w.dirty[id] |= watchLost | limitChanged | eventsChanged
				}
				continue
			}
			watch, ok := w.inotifyWatches[wd]
			if !ok {
				continue
			}
			if mask&unix.IN_IGNORED != 0 {
				delete(w.inotifyWatches, wd)
				if watch.kind == limitFile {
					watch.target.limitWatch = -1
				} else {
					watch.target.eventsWatch = -1
				}
			}
			if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_IGNORED) != 0 {
				w.dirty[watch.target.id] |= watchLost
			}
			if watch.kind == limitFile {
				w.dirty[watch.target.id] |= limitChanged
			} else {
				w.dirty[watch.target.id] |= eventsChanged
			}
		}
	}
	return nil
}

func (w *Watcher) observe(entry *target, change changeFlags) error {
	info, err := os.Lstat(filepath.Join(w.root, entry.path))
	if err != nil {
		return err
	}
	if !os.SameFile(entry.identity, info) {
		return os.ErrNotExist
	}
	if change&watchLost != 0 {
		if w.mode == cgroups.Unified {
			err = w.addV2Target(entry)
		} else {
			err = w.addLimitWatch(entry, "memory.limit_in_bytes")
		}
		if err != nil {
			return err
		}
	}
	high, maxCount := entry.highCount, entry.maxCount
	if w.mode == cgroups.Unified && change&eventsChanged != 0 {
		high, maxCount, err = readCounters(entry.eventsPath)
		if err != nil {
			return err
		}
		if high <= entry.highCount && maxCount <= entry.maxCount &&
			change&(usageChanged|limitChanged|watchLost) == 0 {
			return nil
		}
	}
	usage, err := w.readUsage(entry.path)
	if err != nil {
		return err
	}
	if w.mode != cgroups.Unified && change&(limitChanged|watchLost) != 0 {
		if err := w.rearmV1(entry, usage); err != nil {
			return err
		}
		usage, err = w.readUsage(entry.path)
		if err != nil {
			return err
		}
	}
	entry.highCount, entry.maxCount = high, maxCount
	event := w.observation(entry, usage)
	if event.Kind != EventUnknown {
		return w.publish(entry, &event)
	}
	// A later below-threshold observation invalidates an unread pressure hint.
	w.mu.Lock()
	if w.pending[entry.id] != nil {
		w.removePending(entry)
	}
	w.mu.Unlock()
	return nil
}
