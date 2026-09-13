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

//go:build integration

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"

	"huatuo-bamai/internal/utils/netutil"

	"golang.org/x/sys/unix"
)

const namespaceSamples = 64

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "network namespace cookie probe: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: netnamespace_cookie current | target PID")
	}

	switch os.Args[1] {
	case "current":
		cookie, err := currentNamespaceCookie()
		if errors.Is(err, unix.ENOPROTOOPT) {
			fmt.Println("unsupported")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Println(cookie)
		return nil
	case "target":
		if len(os.Args) != 3 {
			return errors.New("usage: netnamespace_cookie target PID")
		}
		pid, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return fmt.Errorf("parse target pid: %w", err)
		}
		return probeTargetNamespace(pid)
	default:
		return fmt.Errorf("unknown mode %q", os.Args[1])
	}
}

func probeTargetNamespace(pid int) error {
	hostInum, err := namespaceInum("/proc/self/ns/net")
	if err != nil {
		return fmt.Errorf("read host network namespace: %w", err)
	}

	cookie, err := netutil.NetNamespaceCookieByPID(pid)
	if err != nil {
		return fmt.Errorf("read target network namespace cookie: %w", err)
	}
	if err := verifySchedulerNamespaces(hostInum); err != nil {
		return err
	}

	fmt.Println(cookie)
	return nil
}

func currentNamespaceCookie() (uint64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("create cookie socket: %w", err)
	}
	defer unix.Close(fd) //nolint:errcheck // process exits after the probe

	cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if err != nil {
		return 0, fmt.Errorf("read SO_NETNS_COOKIE: %w", err)
	}
	return cookie, nil
}

func verifySchedulerNamespaces(hostInum uint64) error {
	results := make(chan error, namespaceSamples)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(namespaceSamples)

	for range namespaceSamples {
		go func() {
			ready.Done()
			<-start
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			inum, err := namespaceInum("/proc/thread-self/ns/net")
			if err != nil {
				results <- err
				return
			}
			if inum != hostInum {
				results <- fmt.Errorf(
					"scheduler thread network namespace = %d, want host %d",
					inum,
					hostInum,
				)
				return
			}
			results <- nil
		}()
	}

	ready.Wait()
	close(start)
	for range namespaceSamples {
		if err := <-results; err != nil {
			return err
		}
	}
	return nil
}

func namespaceInum(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected stat type %T", info.Sys())
	}
	return stat.Ino, nil
}
