// Copyright 2025, 2026 The HuaTuo Authors
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

package pidfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

var defaultDirPath = "/var/run"

func path(name string) string {
	return fmt.Sprintf("%s/%s.pid", defaultDirPath, name)
}

// Handle owns an acquired pid file: an open fd holding the exclusive
// flock, plus the on-disk path to remove on Unlock. The fd must stay
// reachable for the whole lifetime of the lock — Linux flock is bound
// to the open file description, so if Handle is garbage-collected the
// kernel silently releases the flock while the pid file is still on disk.
type Handle struct {
	file *os.File
	path string
}

// Lock takes an exclusive, non-blocking flock on the pid file for name.
// If another live process already holds the lock, the returned error
// embeds its recorded pid.
func Lock(name string) (*Handle, error) {
	p := path(name)

	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, readErr := os.ReadFile(p)
			if readErr != nil {
				return nil, fmt.Errorf("already running: %s", p)
			}

			return nil, fmt.Errorf("already running: %s pid=%s", p, bytes.TrimSpace(pid))
		}

		return nil, err
	}

	if _, err := fmt.Fprintf(f, "%d", os.Getpid()); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(p)

		return nil, err
	}

	return &Handle{file: f, path: p}, nil
}

// Unlock unlocks, closes, and removes the pid file. Calling Unlock more
// than once is a no-op.
func (h *Handle) Unlock() {
	if h.file == nil {
		return
	}

	_ = syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	_ = h.file.Close()
	_ = os.Remove(h.path)
	h.file = nil
}

// Read reads the "PID file" at path, and returns the PID if it contains a
// valid PID of a running process, or 0 otherwise. It returns an error when
// failing to read the file, or if the file doesn't exist, but malformed content
// is ignored. Consumers should therefore check if the returned PID is a non-zero
// value before use.
func Read(path string) (int, error) {
	pidByte, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(string(bytes.TrimSpace(pidByte)))
}
