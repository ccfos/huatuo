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

package netutil

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// NetNSInodeByPid returns the inode of the network namespace for the given pid.
func NetNSInodeByPid(pid int) (uint64, error) {
	netnsStat, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, err
	}
	return netnsStat.Sys().(*syscall.Stat_t).Ino, nil
}

// NetNSCookieByPid returns the network namespace cookie for the given pid.
// Requires Linux 5.14+ (SO_NETNS_COOKIE). Returns 0, nil on older kernels.
func NetNSCookieByPid(pid int) (uint64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	curNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return 0, fmt.Errorf("open self netns: %w", err)
	}
	defer curNS.Close()

	targetNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, fmt.Errorf("open pid %d netns: %w", pid, err)
	}
	defer targetNS.Close()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		return 0, fmt.Errorf("setns pid %d: %w", pid, err)
	}
	defer unix.Setns(int(curNS.Fd()), unix.CLONE_NEWNET) //nolint:errcheck // best-effort restore

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if err != nil {
		if err == unix.ENOPROTOOPT {
			return 0, nil
		}
		return 0, fmt.Errorf("getsockopt SO_NETNS_COOKIE: %w", err)
	}
	return cookie, nil
}
