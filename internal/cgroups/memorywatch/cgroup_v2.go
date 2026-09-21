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
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/utils/parseutil"
)

func (w *Watcher) addV2Target(entry *target) error {
	path := filepath.Join(w.root, entry.path, "memory.events.local")
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		path = filepath.Join(w.root, entry.path, "memory.events")
	}
	if err := w.addLimitWatch(entry, "memory.max"); err != nil {
		return err
	}
	wd, err := w.addInotify(entry, path, eventsFile)
	if err != nil {
		return err
	}
	oldWatch := entry.eventsWatch
	entry.eventsWatch = wd
	if oldWatch != wd {
		w.removeInotify(oldWatch)
	}
	if entry.eventsPath == path {
		// Recovery of the same source must retain the cumulative baseline.
		return nil
	}
	high, maxCount, err := readCounters(path)
	if err != nil {
		return err
	}
	entry.eventsPath, entry.highCount, entry.maxCount = path, high, maxCount
	return nil
}

func readCounters(path string) (uint64, uint64, error) {
	counts, err := parseutil.RawKV(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read memory events %q: %w", path, err)
	}
	high, hasHigh := counts["high"]
	maxCount, hasMax := counts["max"]
	if !hasHigh || !hasMax {
		return 0, 0, fmt.Errorf("memory events %q requires high and max counters", path)
	}
	return high, maxCount, nil
}
