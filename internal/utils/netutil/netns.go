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
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"github.com/ccfos/huatuo/internal/procfs"

	"golang.org/x/sys/unix"
)

// NetNamespaceInumByPID returns the network namespace inum for pid.
func NetNamespaceInumByPID(pid int) (uint64, error) {
	netnsStat, err := os.Stat(procfs.Path(strconv.Itoa(pid), "ns/net"))
	if err != nil {
		return 0, err
	}
	return netnsStat.Sys().(*syscall.Stat_t).Ino, nil
}

// NetNamespaceCookieByPID returns the network namespace cookie for pid.
// Requires Linux 5.14+ (SO_NETNS_COOKIE). Returns 0, nil on older kernels.
//
// setns changes the namespace of one OS thread, not the whole process. Run the
// switch on a short-lived worker so a restore failure can sacrifice that thread
// instead of leaving its caller bound to the target namespace. A caller whose
// thread already lives in a private network namespace must keep that namespace.
// A restore failure is reported alongside the cookie so callers do not silently
// accept a contaminated thread.
func NetNamespaceCookieByPID(pid int) (uint64, error) {
	done := make(chan netNamespaceCookieResult, 1)
	go func() {
		cookie, err := netNamespaceCookieInPIDNamespace(pid)
		done <- netNamespaceCookieResult{
			cookie: cookie,
			err:    err,
		}
	}()
	result := <-done
	return result.cookie, result.err
}

// restoreNetNamespace is a variable so tests can inject the otherwise
// unreachable restore-failure path.
var restoreNetNamespace = func(fd int) error {
	return unix.Setns(fd, unix.CLONE_NEWNET)
}

type netNamespaceCookieResult struct {
	cookie uint64
	err    error
}

func netNamespaceCookieInPIDNamespace(pid int) (cookie uint64, err error) {
	runtime.LockOSThread()
	unlock := true
	defer func() {
		if unlock {
			runtime.UnlockOSThread()
		}
	}()

	// /proc/self names the thread-group leader. A thread may have entered a
	// different network namespace, so snapshot this worker's own namespace
	// through thread-self; restoring to /proc/self would silently discard it.
	curNS, err := os.Open(procfs.Path("thread-self", "ns/net"))
	if err != nil {
		return 0, fmt.Errorf("open current netns: %w", err)
	}
	defer curNS.Close()

	targetNS, err := os.Open(procfs.Path(strconv.Itoa(pid), "ns/net"))
	if err != nil {
		return 0, fmt.Errorf("open pid %d netns: %w", pid, err)
	}
	defer targetNS.Close()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		return 0, fmt.Errorf("setns pid %d: %w", pid, err)
	}
	defer func() {
		if restoreErr := restoreNetNamespace(int(curNS.Fd())); restoreErr != nil {
			// Exiting while locked makes the runtime terminate this thread.
			// The caller keeps its own clean thread and only sees the error.
			unlock = false
			err = errors.Join(err, fmt.Errorf("restore netns after reading PID %d: %w", pid, restoreErr))
		}
	}()

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	cookie, err = unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if err != nil {
		if errors.Is(err, unix.ENOPROTOOPT) {
			return 0, nil
		}
		return 0, fmt.Errorf("getsockopt SO_NETNS_COOKIE: %w", err)
	}
	return cookie, nil
}
