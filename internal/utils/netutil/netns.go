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
	"syscall"

	"golang.org/x/sys/unix"
)

// NetNamespaceInumByPID returns the network namespace inum for pid.
func NetNamespaceInumByPID(pid int) (uint64, error) {
	netnsStat, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, err
	}
	return netnsStat.Sys().(*syscall.Stat_t).Ino, nil
}

// threadNetNamespace names the net namespace of the calling thread. Reading
// /proc/self here would name the process's main thread namespace instead, so
// the snapshot could belong to a different thread than the one being locked.
const threadNetNamespace = "/proc/thread-self/ns/net"

// netNamespaceFile is the subset of *os.File the namespace worker needs.
type netNamespaceFile interface {
	Fd() uintptr
	Close() error
}

// netNamespaceCookieOps are the syscall edges of the namespace switch.
type netNamespaceCookieOps struct {
	setns  func(fd, nstype int) error
	lock   func()
	unlock func()
	read   func() (uint64, error)
}

// run reads the cookie from targetFd and restores the caller's namespace
// before returning.
//
// The thread stays locked when the restore fails: a goroutine that hands a
// thread carrying the target namespace back to the scheduler lets an unrelated
// caller observe the wrong cookie. Keeping it locked makes the thread exit
// with this goroutine instead.
func (ops netNamespaceCookieOps) run(
	targetFd int,
	openCurrent func() (netNamespaceFile, error),
) (uint64, error) {
	ops.lock()
	inOriginalNamespace := true
	defer func() {
		if inOriginalNamespace {
			ops.unlock()
		}
	}()

	current, err := openCurrent()
	if err != nil {
		return 0, fmt.Errorf("open self netns: %w", err)
	}
	defer current.Close()

	if err := ops.setns(targetFd, unix.CLONE_NEWNET); err != nil {
		return 0, fmt.Errorf("setns target: %w", err)
	}
	inOriginalNamespace = false

	cookie, readErr := ops.read()

	if err := ops.setns(int(current.Fd()), unix.CLONE_NEWNET); err != nil {
		return 0, errors.Join(readErr, fmt.Errorf("restore self netns: %w", err))
	}
	inOriginalNamespace = true

	return cookie, readErr
}

// NetNamespaceCookieByPID returns the network namespace cookie for pid.
// Requires Linux 5.14+ (SO_NETNS_COOKIE). Returns 0, nil on older kernels.
func NetNamespaceCookieByPID(pid int) (uint64, error) {
	targetNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, fmt.Errorf("open pid %d netns: %w", pid, err)
	}
	defer targetNS.Close()

	ops := netNamespaceCookieOps{
		setns:  unix.Setns,
		lock:   runtime.LockOSThread,
		unlock: runtime.UnlockOSThread,
		read:   readNetNamespaceCookie,
	}

	return ops.run(int(targetNS.Fd()), func() (netNamespaceFile, error) {
		return os.Open(threadNetNamespace)
	})
}

// readNetNamespaceCookie reads SO_NETNS_COOKIE from a socket created in the
// caller's current net namespace.
func readNetNamespaceCookie() (uint64, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)

	cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if err != nil {
		if errors.Is(err, unix.ENOPROTOOPT) {
			return 0, nil
		}
		return 0, fmt.Errorf("getsockopt SO_NETNS_COOKIE: %w", err)
	}

	return cookie, nil
}
