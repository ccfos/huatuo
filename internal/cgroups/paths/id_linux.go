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

package paths

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// KernfsID returns the full cgroup v1/v2 ID, including inode generation bits.
// It must be compared within the same hierarchy, not with a stat inode number.
func KernfsID(path string) (uint64, error) {
	// v1 controller mount aliases (e.g. cpu -> cpu,cpuacct) may be symlinks.
	handle, _, err := unix.NameToHandleAt(unix.AT_FDCWD, path, unix.AT_SYMLINK_FOLLOW)
	if err != nil {
		return 0, err
	}
	if handle.Type() == 1 { // Older kernfs uses FILEID_INO32_GEN.
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			return 0, err
		}
		if stat.Type != unix.CGROUP_SUPER_MAGIC && stat.Type != unix.CGROUP2_SUPER_MAGIC {
			return 0, fmt.Errorf("file handle is not from a cgroup filesystem")
		}
	}
	return kernfsHandleID(handle)
}

func kernfsHandleID(handle unix.FileHandle) (uint64, error) {
	data := handle.Bytes()
	if (handle.Type() != 0xfe && handle.Type() != 1) || len(data) != 8 {
		return 0, fmt.Errorf("unexpected kernfs file handle: type %d, size %d", handle.Type(), len(data))
	}
	return binary.NativeEndian.Uint64(data), nil
}
