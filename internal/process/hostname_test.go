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

package process

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// stubSetns replaces the namespace switch with a recorder that reports the
// namespace file each call would enter, so the enter and restore sequence can
// be asserted without CAP_SYS_ADMIN.
func stubSetns(t *testing.T, fail func(attempt int) error) *[]string {
	t.Helper()

	original := setns
	entered := make([]string, 0, 2)
	attempt := 0

	setns = func(fd, nstype int) error {
		attempt++
		if err := fail(attempt); err != nil {
			return err
		}
		if nstype != unix.CLONE_NEWUTS {
			t.Errorf("setns nstype = %d, want CLONE_NEWUTS", nstype)
		}
		entered = append(entered, readNamespaceMarker(t, fd))
		return nil
	}
	t.Cleanup(func() { setns = original })

	return &entered
}

func readNamespaceMarker(t *testing.T, fd int) string {
	t.Helper()

	buffer := make([]byte, 16)
	read, err := unix.Pread(fd, buffer, 0)
	if err != nil {
		t.Fatalf("Pread(namespace fd %d) error = %v", fd, err)
	}

	return string(buffer[:read])
}

// writeUTSNamespace writes a readable stand-in for /proc/<pid>/ns/uts. The
// content identifies the namespace, so a stubbed setns can report which
// namespace a call entered.
func writeUTSNamespace(t *testing.T, root, pid string) {
	t.Helper()

	namespacePath := filepath.Join(root, "proc", pid, "ns", "uts")
	if err := os.MkdirAll(filepath.Dir(namespacePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(namespacePath), err)
	}
	if err := os.WriteFile(namespacePath, []byte(pid), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", namespacePath, err)
	}
}

func noSetnsFailure(int) error { return nil }

func TestHostnameRestoresCallerNamespace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespace lookup requires Linux")
	}

	root := setProcRoot(t)
	writeUTSNamespace(t, root, "thread-self")
	writeUTSNamespace(t, root, "4242")

	entered := stubSetns(t, noSetnsFailure)

	hostname, err := Hostname(4242)
	if err != nil {
		t.Fatalf("Hostname(4242) error = %v", err)
	}
	if hostname == "" {
		t.Fatal("Hostname(4242) = \"\", want the host hostname")
	}

	want := []string{"4242", "thread-self"}
	if len(*entered) != len(want) {
		t.Fatalf("namespaces entered = %v, want %v", *entered, want)
	}
	for i := range want {
		if (*entered)[i] != want[i] {
			t.Fatalf("namespaces entered = %v, want %v", *entered, want)
		}
	}
}

func TestHostnameRequiresCallerNamespace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespace lookup requires Linux")
	}

	root := setProcRoot(t)
	writeUTSNamespace(t, root, "4242")

	entered := stubSetns(t, noSetnsFailure)

	if _, err := Hostname(4242); err == nil {
		t.Fatal("Hostname(4242) error = nil, want an error for the missing caller namespace")
	}
	if len(*entered) != 0 {
		t.Fatalf("namespaces entered = %v, want none before the caller namespace is resolved", *entered)
	}
}

func TestHostnameSkipsRestoreWhenEnterFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespace lookup requires Linux")
	}

	root := setProcRoot(t)
	writeUTSNamespace(t, root, "thread-self")
	writeUTSNamespace(t, root, "4242")

	attempts := 0
	stubSetns(t, func(int) error {
		attempts++
		return errors.New("setns denied")
	})

	if _, err := Hostname(4242); err == nil {
		t.Fatal("Hostname(4242) error = nil, want the enter failure")
	}
	if attempts != 1 {
		t.Fatalf("setns attempts = %d, want 1 (the namespace was never entered)", attempts)
	}
}

func TestHostnameReportsRestoreFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespace lookup requires Linux")
	}

	root := setProcRoot(t)
	writeUTSNamespace(t, root, "thread-self")
	writeUTSNamespace(t, root, "4242")

	stubSetns(t, func(attempt int) error {
		if attempt == 2 {
			return errors.New("setns denied")
		}
		return nil
	})

	hostname, err := Hostname(4242)
	if err == nil || !strings.Contains(err.Error(), "restore UTS namespace") {
		t.Fatalf("Hostname(4242) error = %v, want a restore failure", err)
	}
	if hostname == "" {
		t.Fatal("Hostname(4242) = \"\", want the observed hostname alongside the restore failure")
	}
}

// utsNamespaceTargetHostname and utsNamespaceCallerHostname name the two
// namespaces the kernel-level test switches between, so a leaked namespace
// shows up as a wrong hostname in the caller's thread.
const (
	utsNamespaceTargetHostname = "huatuo-uts-target"
	utsNamespaceCallerHostname = "huatuo-uts-caller"
)

// utsNamespaceThread is a helper goroutine whose OS thread lives in its own UTS
// namespace for as long as the test needs it.
type utsNamespaceThread struct {
	tid      int
	hostname string
	release  chan struct{}
	stopped  chan error
}

// startUTSNamespaceThread moves an OS thread into a fresh UTS namespace, sets
// hostname in it and returns the thread, so a test can point Hostname at a
// target namespace that really differs from the caller's. The thread is moved
// back before it is unlocked, so a failure cannot leak a foreign namespace into
// the runtime's thread pool.
func startUTSNamespaceThread(t *testing.T, hostname string) *utsNamespaceThread {
	t.Helper()

	started := make(chan int, 1)
	failed := make(chan error, 1)
	release := make(chan struct{})
	stopped := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// The absolute path is used on purpose: the kernel-level test must act
		// on the real namespaces, not on the /proc stand-in the stubbed tests
		// install with setProcRoot.
		callerNS, err := os.Open("/proc/thread-self/ns/uts")
		if err != nil {
			failed <- err
			return
		}
		defer callerNS.Close()

		if err := unix.Unshare(unix.CLONE_NEWUTS); err != nil {
			failed <- err
			return
		}
		if err := unix.Sethostname([]byte(hostname)); err != nil {
			// Leave the fresh namespace before the thread goes back to the pool.
			failed <- errors.Join(err, unix.Setns(int(callerNS.Fd()), unix.CLONE_NEWUTS))
			return
		}

		started <- unix.Gettid()
		<-release

		stopped <- unix.Setns(int(callerNS.Fd()), unix.CLONE_NEWUTS)
	}()

	thread := &utsNamespaceThread{hostname: hostname, release: release, stopped: stopped}
	select {
	case thread.tid = <-started:
	case err := <-failed:
		t.Skipf("UTS namespaces are unavailable: %v", err)
	}
	t.Cleanup(func() {
		close(thread.release)
		if err := <-thread.stopped; err != nil {
			t.Errorf("restore helper UTS namespace: %v", err)
		}
	})

	return thread
}

// enter moves the calling OS thread into thread's UTS namespace. The caller
// must already have locked its OS thread.
func (thread *utsNamespaceThread) enter(t *testing.T) {
	t.Helper()

	namespace, err := os.Open("/proc/" + strconv.Itoa(thread.tid) + "/ns/uts")
	if err != nil {
		t.Skipf("open the namespace of thread %d: %v", thread.tid, err)
	}
	defer namespace.Close()

	if err := unix.Setns(int(namespace.Fd()), unix.CLONE_NEWUTS); err != nil {
		t.Skipf("enter the namespace of thread %d: %v", thread.tid, err)
	}
}

// callerNamespace describes the UTS namespace of the calling OS thread, which
// must be locked by the caller.
func callerNamespace(t *testing.T) (hostname, namespace string) {
	t.Helper()

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}

	namespace, err = os.Readlink("/proc/thread-self/ns/uts")
	if err != nil {
		t.Fatalf("Readlink(/proc/thread-self/ns/uts) error = %v", err)
	}

	return hostname, namespace
}

// TestHostnameRestoresCallerThreadOnKernel exercises the real kernel namespace
// transition. The lookup runs on a locked OS thread whose own UTS namespace is
// neither the thread group's nor the target's, which is what a long-lived
// daemon thread looks like after an earlier namespace switch: Hostname must
// leave that thread in its own namespace, otherwise the thread keeps reporting
// the container hostname once the runtime reuses it.
func TestHostnameRestoresCallerThreadOnKernel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespaces require Linux")
	}
	if os.Geteuid() != 0 {
		t.Skip("switching UTS namespaces requires CAP_SYS_ADMIN")
	}

	target := startUTSNamespaceThread(t, utsNamespaceTargetHostname)
	caller := startUTSNamespaceThread(t, utsNamespaceCallerHostname)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	originalNS, err := os.Open("/proc/thread-self/ns/uts")
	if err != nil {
		t.Fatalf("open the caller namespace: %v", err)
	}
	defer originalNS.Close()
	// The test thread is restored before it is unlocked, so the test cannot
	// leak a namespace into the runtime's thread pool.
	defer func() {
		if err := unix.Setns(int(originalNS.Fd()), unix.CLONE_NEWUTS); err != nil {
			t.Errorf("restore the test thread: %v", err)
		}
	}()

	caller.enter(t)

	hostnameBefore, namespaceBefore := callerNamespace(t)
	if hostnameBefore != caller.hostname {
		t.Fatalf("test thread hostname = %q, want %q", hostnameBefore, caller.hostname)
	}

	hostname, err := Hostname(target.tid)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skipf("UTS namespaces are not joinable: %v", err)
		}
		t.Fatalf("Hostname(%d) error = %v", target.tid, err)
	}
	if hostname != target.hostname {
		t.Fatalf("Hostname(%d) = %q, want %q", target.tid, hostname, target.hostname)
	}

	hostnameAfter, namespaceAfter := callerNamespace(t)
	if hostnameAfter != hostnameBefore || namespaceAfter != namespaceBefore {
		t.Fatalf("the caller thread left its own UTS namespace: %q (%s) -> %q (%s)",
			hostnameBefore, namespaceBefore, hostnameAfter, namespaceAfter)
	}
}
