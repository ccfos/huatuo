// Copyright 2025 The HuaTuo Authors
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

package pids

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
)

// Process-level files:
// cgroup v1: /sys/fs/cgroup/$GROUPPATH/cgroup.procs
// cgroup v2: /sys/fs/cgroup/$GROUPPATH/cgroup.procs

// Thread-level files:
// cgroup v1: /sys/fs/cgroup/$GROUPPATH/tasks
// cgroup v2: /sys/fs/cgroup/$GROUPPATH/cgroup.thread
func Tasks(path, file string) ([]int32, error) {
	f, err := os.Open(filepath.Join(path, file))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		out []int32
		s   = bufio.NewScanner(f)
	)

	for s.Scan() {
		if t := s.Text(); t != "" {
			pid, err := strconv.ParseInt(t, 10, 0)
			if err != nil {
				return nil, err
			}
			out = append(out, int32(pid))
		}
	}

	if err := s.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
