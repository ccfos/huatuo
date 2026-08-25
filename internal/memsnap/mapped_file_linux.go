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

package memsnap

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenMappedFile pins a regular file matching the mapping before opening it
// for reading. O_PATH avoids opening replacement FIFOs or devices for I/O.
// This checks the inode, not immutable contents or a hard I/O deadline.
// Device IDs are not compared: Btrfs can report different IDs in maps and stat.
func OpenMappedFile(path string, mapping ProcMap) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("mapped file %s is not a regular file", path)
	}
	if mapping.Inode == 0 || stat.Ino != mapping.Inode {
		return nil, fmt.Errorf("mapped file %s no longer matches process maps", path)
	}
	// Reopen the pinned object, never the target-controlled pathname.
	return os.Open(fmt.Sprintf("/proc/self/fd/%d", fd))
}
