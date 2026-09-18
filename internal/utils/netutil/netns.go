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

const currentThreadNetNamespace = "/proc/thread-self/ns/net"

type namespaceHandle interface {
	Fd() uintptr
	Close() error
}

type netNamespaceCookieOperations struct {
	open       func(string) (namespaceHandle, error)
	setns      func(int, int) error
	socket     func(int, int, int) (int, error)
	getsockopt func(int, int, int) (uint64, error)
	close      func(int) error
}

type netNamespaceThreadOperations struct {
	lock   func()
	unlock func()
}

type netNamespaceCookieResult struct {
	cookie        uint64
	err           error
	discardThread bool
}

// NetNamespaceInumByPID returns the network namespace inum for pid.
func NetNamespaceInumByPID(pid int) (uint64, error) {
	netnsStat, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, err
	}
	return netnsStat.Sys().(*syscall.Stat_t).Ino, nil
}

// NetNamespaceCookieByPID returns the network namespace cookie for pid.
// Requires Linux 5.14+ (SO_NETNS_COOKIE). Returns 0, nil on older kernels.
func NetNamespaceCookieByPID(pid int) (uint64, error) {
	targetPath := fmt.Sprintf("/proc/%d/ns/net", pid)
	return netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open: func(path string) (namespaceHandle, error) {
			return os.Open(path)
		},
		setns:      unix.Setns,
		socket:     unix.Socket,
		getsockopt: unix.GetsockoptUint64,
		close:      unix.Close,
	})
}

func netNamespaceCookie(
	targetPath string,
	operations netNamespaceCookieOperations,
) (uint64, error) {
	return netNamespaceCookieWithThreadOperations(
		targetPath,
		operations,
		netNamespaceThreadOperations{
			lock:   runtime.LockOSThread,
			unlock: runtime.UnlockOSThread,
		},
	)
}

func netNamespaceCookieWithThreadOperations(
	targetPath string,
	operations netNamespaceCookieOperations,
	threadOperations netNamespaceThreadOperations,
) (uint64, error) {
	resultCh := make(chan netNamespaceCookieResult, 1)
	go func() {
		threadOperations.lock()
		result := readNetNamespaceCookie(targetPath, operations)
		if !result.discardThread {
			threadOperations.unlock()
		}
		resultCh <- result
		// A goroutine that exits while locked takes a namespace-contaminated
		// operating-system thread out of the scheduler pool.
	}()

	result := <-resultCh
	return result.cookie, result.err
}

func readNetNamespaceCookie(
	targetPath string,
	operations netNamespaceCookieOperations,
) netNamespaceCookieResult {
	current, err := operations.open(currentThreadNetNamespace)
	if err != nil {
		return netNamespaceCookieResult{
			err: fmt.Errorf("open current thread network namespace: %w", err),
		}
	}

	target, err := operations.open(targetPath)
	if err != nil {
		return netNamespaceCookieResult{err: errors.Join(
			fmt.Errorf("open target network namespace %q: %w", targetPath, err),
			closeNamespaceHandle(current, "current thread network namespace"),
		)}
	}

	if err := operations.setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		return netNamespaceCookieResult{err: errors.Join(
			fmt.Errorf("enter target network namespace %q: %w", targetPath, err),
			closeNamespaceHandle(target, "target network namespace"),
			closeNamespaceHandle(current, "current thread network namespace"),
		)}
	}

	cookie, operationErr := readCookieFromSocket(operations)
	restoreErr := operations.setns(int(current.Fd()), unix.CLONE_NEWNET)
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore current thread network namespace: %w", restoreErr)
	}

	return netNamespaceCookieResult{
		cookie: cookie,
		err: errors.Join(
			operationErr,
			restoreErr,
			closeNamespaceHandle(target, "target network namespace"),
			closeNamespaceHandle(current, "current thread network namespace"),
		),
		discardThread: restoreErr != nil,
	}
}

func readCookieFromSocket(operations netNamespaceCookieOperations) (uint64, error) {
	fd, err := operations.socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("create network namespace cookie socket: %w", err)
	}

	cookie, cookieErr := operations.getsockopt(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if errors.Is(cookieErr, unix.ENOPROTOOPT) {
		cookieErr = nil
		cookie = 0
	} else if cookieErr != nil {
		cookieErr = fmt.Errorf("read SO_NETNS_COOKIE: %w", cookieErr)
	}
	closeErr := operations.close(fd)
	if closeErr != nil {
		closeErr = fmt.Errorf("close network namespace cookie socket: %w", closeErr)
	}
	return cookie, errors.Join(cookieErr, closeErr)
}

func closeNamespaceHandle(handle namespaceHandle, description string) error {
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close %s: %w", description, err)
	}
	return nil
}
