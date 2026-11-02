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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNetNamespaceInumByPID(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name:    "valid current pid",
			pid:     os.Getpid(),
			wantErr: false,
		},
		{
			name:    "invalid pid 0",
			pid:     0,
			wantErr: true,
		},
		{
			name:    "invalid negative pid",
			pid:     -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NetNamespaceInumByPID(tt.pid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NetNamespaceInumByPID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("NetNamespaceInumByPID() got = %v, want non-zero inum", got)
			}
		})
	}
}

const (
	netNamespaceModeEnv      = "HUATUO_TEST_NET_NAMESPACE_MODE"
	netNamespaceTargetPIDEnv = "HUATUO_TEST_NET_NAMESPACE_TARGET_PID"
	netNamespaceExpectedEnv  = "HUATUO_TEST_NET_NAMESPACE_EXPECTED_COOKIE"
)

func TestNetNamespaceCookieByPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("network namespace lookup requires Linux")
	}

	switch os.Getenv(netNamespaceModeEnv) {
	case "target":
		runNetNamespaceTarget()
		return
	case "caller":
		runNetNamespaceCaller()
		return
	case "namespaced":
		runNetNamespaceTestSuite(t)
		return
	}

	if _, err := NetNamespaceCookieByPID(os.Getpid()); errors.Is(err, syscall.EPERM) {
		t.Log("setns requires CAP_SYS_ADMIN; retrying in a user namespace")
		runNetNamespaceTestsInUserNamespace(t)
		return
	}
	runNetNamespaceTestSuite(t)
}

func runNetNamespaceTestSuite(t *testing.T) {
	t.Helper()

	targetPID, targetCookie := startNetNamespaceTarget(t)
	t.Run("restores caller namespace", func(t *testing.T) {
		testNetNamespaceRestoresCallerNamespace(t, targetPID, targetCookie)
	})
	t.Run("restore failure leaves caller unchanged", func(t *testing.T) {
		testNetNamespaceRestoreFailureKeepsCaller(t, targetPID, targetCookie)
	})
	t.Run("invalid pid leaves caller unchanged", testNetNamespaceInvalidPIDKeepsCaller)
}

func runNetNamespaceTestsInUserNamespace(t *testing.T) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.v", "-test.run=^TestNetNamespaceCookieByPID$")
	cmd.Env = append(os.Environ(), netNamespaceModeEnv+"=namespaced")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
		GidMappingsEnableSetgroups: false,
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("creating a user namespace requires permission")
		}
		t.Fatalf("namespaced network namespace tests failed: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte("--- SKIP")) {
		t.Skip("namespaced network namespace tests were skipped")
	}
}

func testNetNamespaceRestoresCallerNamespace(t *testing.T, targetPID int, targetCookie uint64) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestNetNamespaceCookieByPID$")
	cmd.Env = append(
		os.Environ(),
		netNamespaceModeEnv+"=caller",
		netNamespaceTargetPIDEnv+"="+strconv.Itoa(targetPID),
		netNamespaceExpectedEnv+"="+strconv.FormatUint(targetCookie, 10),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}

	output, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 77 {
			t.Skipf("caller helper skipped: %s", output)
		}
		if errors.Is(err, syscall.EPERM) {
			t.Skip("creating a network namespace requires CAP_SYS_ADMIN")
		}
		t.Fatalf("caller helper failed: %v\n%s", err, output)
	}
}

func testNetNamespaceRestoreFailureKeepsCaller(t *testing.T, targetPID int, targetCookie uint64) {
	t.Helper()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	beforeNS, err := currentThreadNetNamespace()
	if err != nil {
		t.Fatalf("read caller namespace before NetNamespaceCookieByPID: %v", err)
	}
	beforeCookie, err := currentThreadNetNamespaceCookie()
	if err != nil {
		t.Fatalf("read caller cookie before NetNamespaceCookieByPID: %v", err)
	}

	originalRestore := restoreNetNamespace
	t.Cleanup(func() { restoreNetNamespace = originalRestore })
	restoreNetNamespace = func(int) error { return errors.New("injected restore failure") }

	for range 3 {
		cookie, err := NetNamespaceCookieByPID(targetPID)
		if err == nil || !strings.Contains(err.Error(), "restore netns") {
			t.Fatalf("NetNamespaceCookieByPID(%d) error = %v, want restore failure", targetPID, err)
		}
		if cookie != targetCookie {
			t.Fatalf("NetNamespaceCookieByPID(%d) = %d, %v, want target cookie %d", targetPID, cookie, err, targetCookie)
		}
		afterNS, nsErr := currentThreadNetNamespace()
		if nsErr != nil {
			t.Fatalf("read caller namespace after failed restore: %v", nsErr)
		}
		if afterNS != beforeNS {
			t.Fatalf("caller namespace changed after failed restore: %q -> %q", beforeNS, afterNS)
		}
		afterCookie, cookieErr := currentThreadNetNamespaceCookie()
		if cookieErr != nil {
			t.Fatalf("read caller cookie after failed restore: %v", cookieErr)
		}
		if afterCookie != beforeCookie {
			t.Fatalf("caller cookie changed after failed restore: %d -> %d", beforeCookie, afterCookie)
		}
	}

	restoreNetNamespace = originalRestore
	got, err := NetNamespaceCookieByPID(targetPID)
	if err != nil {
		t.Fatalf("NetNamespaceCookieByPID(%d) after restore hook reset error = %v", targetPID, err)
	}
	if got != targetCookie {
		t.Fatalf("NetNamespaceCookieByPID(%d) after restore hook reset = %d, want %d", targetPID, got, targetCookie)
	}
	afterNS, err := currentThreadNetNamespace()
	if err != nil {
		t.Fatalf("read caller namespace after successful call: %v", err)
	}
	if afterNS != beforeNS {
		t.Fatalf("caller namespace changed after successful call: %q -> %q", beforeNS, afterNS)
	}
	afterCookie, err := currentThreadNetNamespaceCookie()
	if err != nil {
		t.Fatalf("read caller cookie after successful call: %v", err)
	}
	if afterCookie != beforeCookie {
		t.Fatalf("caller cookie changed after successful call: %d -> %d", beforeCookie, afterCookie)
	}
}

func testNetNamespaceInvalidPIDKeepsCaller(t *testing.T) {
	t.Helper()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	beforeNS, err := currentThreadNetNamespace()
	if err != nil {
		t.Fatalf("read caller namespace before invalid PID lookup: %v", err)
	}
	beforeCookie, err := currentThreadNetNamespaceCookie()
	if err != nil {
		t.Fatalf("read caller cookie before invalid PID lookup: %v", err)
	}

	if cookie, err := NetNamespaceCookieByPID(-1); err == nil || cookie != 0 {
		t.Fatalf("NetNamespaceCookieByPID(-1) = %d, %v, want zero result and error", cookie, err)
	}

	afterNS, err := currentThreadNetNamespace()
	if err != nil {
		t.Fatalf("read caller namespace after invalid PID lookup: %v", err)
	}
	if afterNS != beforeNS {
		t.Fatalf("caller namespace changed after invalid PID lookup: %q -> %q", beforeNS, afterNS)
	}
	afterCookie, err := currentThreadNetNamespaceCookie()
	if err != nil {
		t.Fatalf("read caller cookie after invalid PID lookup: %v", err)
	}
	if afterCookie != beforeCookie {
		t.Fatalf("caller cookie changed after invalid PID lookup: %d -> %d", beforeCookie, afterCookie)
	}
}

func startNetNamespaceTarget(t *testing.T) (int, uint64) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestNetNamespaceCookieByPID$")
	cmd.Env = append(os.Environ(), netNamespaceModeEnv+"=target")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("creating a network namespace requires CAP_SYS_ADMIN")
		}
		t.Fatalf("start network namespace helper: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil && !t.Failed() {
			t.Errorf("network namespace helper: %v", err)
		}
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("network namespace helper produced no status: %v", scanner.Err())
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) != 2 || fields[0] != "ready" {
		t.Fatalf("network namespace helper status = %q, want %q", scanner.Text(), "ready <cookie>")
	}
	cookie, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("parse network namespace cookie %q: %v", fields[1], err)
	}
	if cookie == 0 {
		t.Skip("SO_NETNS_COOKIE is unavailable; requires Linux 5.14+")
	}
	return cmd.Process.Pid, cookie
}

func runNetNamespaceTarget() {
	cookie, err := currentThreadNetNamespaceCookie()
	if err != nil {
		fmt.Fprintf(os.Stderr, "read helper network namespace cookie: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ready %d\n", cookie)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func runNetNamespaceCaller() {
	targetPID, err := strconv.Atoi(os.Getenv(netNamespaceTargetPIDEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse target PID: %v\n", err)
		os.Exit(1)
	}
	expected, err := strconv.ParseUint(os.Getenv(netNamespaceExpectedEnv), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse expected cookie: %v\n", err)
		os.Exit(1)
	}

	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			done <- err
			return
		}
		// Do not unlock after unshare. This helper exits immediately; leaving
		// the private namespace on a runtime-reused thread would contaminate it.
		beforeNS, err := currentThreadNetNamespace()
		if err != nil {
			done <- err
			return
		}
		beforeCookie, err := currentThreadNetNamespaceCookie()
		if err != nil {
			done <- err
			return
		}
		if beforeCookie == 0 {
			done <- errors.New("SO_NETNS_COOKIE is unavailable")
			return
		}
		got, err := NetNamespaceCookieByPID(targetPID)
		if err != nil {
			done <- err
			return
		}
		afterNS, err := currentThreadNetNamespace()
		if err != nil {
			done <- err
			return
		}
		afterCookie, err := currentThreadNetNamespaceCookie()
		if err != nil {
			done <- err
			return
		}
		if got != expected {
			done <- fmt.Errorf("NetNamespaceCookieByPID(%d) = %d, want %d", targetPID, got, expected)
			return
		}
		if afterNS != beforeNS {
			done <- fmt.Errorf("caller namespace changed: %q -> %q", beforeNS, afterNS)
			return
		}
		if afterCookie != beforeCookie {
			done <- fmt.Errorf("caller cookie changed: %d -> %d", beforeCookie, afterCookie)
			return
		}

		// Exercise the namespace-switching body on the private caller thread
		// as well. This is the case where /proc/self names the thread-group
		// leader instead of the thread's own network namespace.
		directCookie, err := netNamespaceCookieInPIDNamespace(targetPID)
		if err != nil {
			done <- fmt.Errorf("netNamespaceCookieInPIDNamespace(%d): %w", targetPID, err)
			return
		}
		directNS, err := currentThreadNetNamespace()
		if err != nil {
			done <- err
			return
		}
		if directCookie != expected {
			done <- fmt.Errorf("netNamespaceCookieInPIDNamespace(%d) = %d, want %d", targetPID, directCookie, expected)
			return
		}
		if directNS != beforeNS {
			done <- fmt.Errorf("direct caller namespace changed: %q -> %q", beforeNS, directNS)
			return
		}
		directAfterCookie, err := currentThreadNetNamespaceCookie()
		if err != nil {
			done <- err
			return
		}
		if directAfterCookie != beforeCookie {
			done <- fmt.Errorf("direct caller cookie changed: %d -> %d", beforeCookie, directAfterCookie)
			return
		}
		done <- nil
	}()

	if err := <-done; err != nil {
		if errors.Is(err, syscall.EPERM) {
			fmt.Printf("skip: %v\n", err)
			os.Exit(77)
		}
		fmt.Fprintf(os.Stderr, "fail: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ok")
	os.Exit(0)
}

func currentThreadNetNamespace() (string, error) {
	return os.Readlink("/proc/thread-self/ns/net")
}

func currentThreadNetNamespaceCookie() (uint64, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)

	cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	if err != nil {
		if errors.Is(err, unix.ENOPROTOOPT) {
			return 0, nil
		}
		return 0, err
	}
	return cookie, nil
}
