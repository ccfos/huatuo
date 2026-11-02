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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/procfs"

	"golang.org/x/sys/unix"
)

func TestExecutable(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}
	if err := os.Symlink("/usr/bin/python3", filepath.Join(procPath, "exe")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	executable, err := Executable(100)
	if err != nil {
		t.Fatalf("Executable(100) error = %v", err)
	}
	if executable != "/usr/bin/python3" {
		t.Fatalf("Executable(100) = %q, want %q", executable, "/usr/bin/python3")
	}
}

func TestExecutableMissing(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := Executable(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Executable(100) error = %v, want fs.ErrNotExist", err)
	}
}

func TestTrimUnlinkedExecutable(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/usr/bin/python3.10", want: "/usr/bin/python3.10"},
		{path: "/usr/bin/python3.10 (deleted)", want: "/usr/bin/python3.10"},
		{path: "/opt/python (deleted)/bin/python", want: "/opt/python (deleted)/bin/python"},
		{path: "/tmp/python (deleted) (deleted)", want: "/tmp/python (deleted)"},
		{path: "/usr/bin/python3.10 (deleted) ", want: "/usr/bin/python3.10 (deleted) "},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := TrimUnlinkedExecutable(test.path); got != test.want {
				t.Errorf("TrimUnlinkedExecutable(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestExecutableName(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "plain executable", target: "/usr/bin/java", want: "java"},
		{name: "unlinked python", target: "/usr/bin/python3.10 (deleted)", want: "python3.10"},
		{name: "unlinked platform python", target: "/usr/libexec/platform-python3.6 (deleted)", want: "platform-python3.6"},
		{name: "unrelated executable", target: "/usr/bin/ruby3.1 (deleted)", want: "ruby3.1"},
		{name: "marker outside the basename", target: "/opt/(deleted)/python3", want: "python3"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpRoot := setProcRoot(t)
			procPath := filepath.Join(tmpRoot, "proc", "100")
			if err := os.MkdirAll(procPath, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
			}
			if err := os.Symlink(test.target, filepath.Join(procPath, "exe")); err != nil {
				t.Fatalf("Symlink(%q) error = %v", test.target, err)
			}

			got, err := ExecutableName(100)
			if err != nil {
				t.Fatalf("ExecutableName(100) error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ExecutableName(100) = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecutableNameMissing(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := ExecutableName(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ExecutableName(100) error = %v, want fs.ErrNotExist", err)
	}
}

func TestPPID(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	stat := "100 (python3) S 42 " + strings.Repeat("0 ", 40)
	if err := os.WriteFile(filepath.Join(procPath, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatalf("WriteFile(stat) error = %v", err)
	}

	ppid, err := PPID(100)
	if err != nil {
		t.Fatalf("PPID(100) error = %v", err)
	}
	if ppid != 42 {
		t.Fatalf("PPID(100) = %d, want 42", ppid)
	}
}

func TestPPIDMissing(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := PPID(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("PPID(100) error = %v, want fs.ErrNotExist", err)
	}
}

func TestCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{
			name: "multiple arguments",
			data: []byte("/usr/bin/docker\x00run\x00--rm\x00alpine\x00"),
			want: "/usr/bin/docker run --rm alpine ",
		},
		{
			name: "empty command line",
		},
		{
			name: "truncate and sanitize",
			data: func() []byte {
				data := bytes.Repeat([]byte{'a'}, 130)
				data[5] = 0
				data[127] = 0
				return data
			}(),
			want: strings.Repeat("a", 5) + " " + strings.Repeat("a", 121) + " ",
		},
		{
			name: "exact limit",
			data: bytes.Repeat([]byte{'a'}, 128),
			want: strings.Repeat("a", 128),
		},
		{
			name: "large command line",
			data: bytes.Repeat([]byte{'a'}, 1024*1024),
			want: strings.Repeat("a", 128),
		},
		{
			name:    "missing process",
			wantErr: true,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpRoot := setProcRoot(t)
			pid := 100 + index
			if !test.wantErr {
				cmdlinePath := filepath.Join(tmpRoot, "proc", strconv.Itoa(pid), "cmdline")
				if err := os.MkdirAll(filepath.Dir(cmdlinePath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(cmdlinePath), err)
				}
				if err := os.WriteFile(cmdlinePath, test.data, 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", cmdlinePath, err)
				}
			}

			got, err := CommandLine(pid)
			if test.wantErr {
				if err == nil {
					t.Fatalf("CommandLine(%d) error = nil, want non-nil", pid)
				}
				return
			}
			if err != nil {
				t.Fatalf("CommandLine(%d) error = %v", pid, err)
			}
			if got != test.want {
				t.Fatalf("CommandLine(%d) = %q, want %q", pid, got, test.want)
			}
		})
	}
}

const (
	utsNamespaceModeEnv        = "HUATUO_TEST_UTS_NAMESPACE_MODE"
	utsNamespaceTargetPIDEnv   = "HUATUO_TEST_UTS_NAMESPACE_TARGET_PID"
	utsNamespaceHelperHostname = "huatuo-uts-namespace-test"
	utsNamespaceCallerHostname = "huatuo-uts-caller-test"
)

func TestHostname(t *testing.T) {
	switch os.Getenv(utsNamespaceModeEnv) {
	case "target":
		runUTSNamespaceTarget()
		return
	case "caller":
		runUTSNamespaceCaller()
		return
	case "namespaced":
		runHostnameTestSuite(t)
		return
	}

	pid := os.Getpid()
	if _, err := Hostname(pid); errors.Is(err, syscall.EPERM) {
		t.Log("setns requires CAP_SYS_ADMIN; retrying in a user namespace")
		runHostnameTestsInUserNamespace(t)
		return
	}
	runHostnameTestSuite(t)
}

func runHostnameTestSuite(t *testing.T) {
	t.Helper()

	pid := os.Getpid()
	want, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname() error = %v", err)
	}
	got, err := Hostname(pid)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("setns requires CAP_SYS_ADMIN")
	}
	if err != nil {
		t.Fatalf("Hostname(%d) error = %v", pid, err)
	}
	if got != want {
		t.Fatalf("Hostname(%d) = %q, want %q", pid, got, want)
	}

	if got, err := Hostname(99999999); err == nil || got != "" {
		t.Fatalf("Hostname(missing) = %q, %v, want empty result and error", got, err)
	}

	t.Run("restores caller namespace", testHostnameRestoresCallerNamespace)
	t.Run("restore failure leaves caller unchanged", testHostnameRestoreFailureKeepsCaller)
}

func runHostnameTestsInUserNamespace(t *testing.T) {
	t.Helper()

	// Skip records are only emitted in verbose mode, including in child tests.
	cmd, _ := utsNamespaceCommand(t)
	cmd.Env = append(os.Environ(), utsNamespaceModeEnv+"=namespaced")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWUTS,
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
		t.Fatalf("namespaced Hostname tests failed: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte("--- SKIP")) {
		t.Skipf("namespaced Hostname tests were skipped:\n%s", output)
	}
}

func testHostnameRestoresCallerNamespace(t *testing.T) {
	t.Helper()
	targetPID := startUTSNamespaceTarget(t)
	runUTSNamespaceCallerTest(t, targetPID)
}

func testHostnameRestoreFailureKeepsCaller(t *testing.T) {
	t.Helper()
	targetPID := startUTSNamespaceTarget(t)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	beforeNS, err := currentThreadUTSNamespace()
	if err != nil {
		t.Fatalf("read caller namespace before Hostname: %v", err)
	}
	beforeHost, err := os.Hostname()
	if err != nil {
		t.Fatalf("read caller hostname before Hostname: %v", err)
	}

	originalRestore := restoreUTSNamespace
	t.Cleanup(func() { restoreUTSNamespace = originalRestore })
	var workerTID int
	restoreFailure := errors.New("injected restore failure")
	restoreUTSNamespace = func(int) error {
		workerTID = unix.Gettid()
		return restoreFailure
	}

	retiredWorkers := 0
	for range 3 {
		_, err := Hostname(targetPID)
		if !errors.Is(err, restoreFailure) {
			t.Fatalf("Hostname(%d) error = %v, want restore failure", targetPID, err)
		}

		// The runtime permanently parks its initial thread instead of exiting
		// it. Other locked workers must disappear after sending their result.
		threadPath := procfs.Path("self", "task", strconv.Itoa(workerTID))
		deadline := time.Now().Add(5 * time.Second)
		for workerTID != os.Getpid() {
			_, statErr := os.Stat(threadPath)
			if errors.Is(statErr, fs.ErrNotExist) {
				retiredWorkers++
				break
			}
			if statErr != nil {
				t.Fatalf("stat worker thread %d: %v", workerTID, statErr)
			}
			if time.Now().After(deadline) {
				t.Fatalf("worker thread %d still exists after restore failure", workerTID)
			}
			time.Sleep(time.Millisecond)
		}

		afterNS, nsErr := currentThreadUTSNamespace()
		if nsErr != nil {
			t.Fatalf("read caller namespace after failed restore: %v", nsErr)
		}
		if afterNS != beforeNS {
			t.Fatalf("caller namespace changed after failed restore: %q -> %q", beforeNS, afterNS)
		}
		afterHost, hostErr := os.Hostname()
		if hostErr != nil {
			t.Fatalf("read caller hostname after failed restore: %v", hostErr)
		}
		if afterHost != beforeHost {
			t.Fatalf("caller hostname changed after failed restore: %q -> %q", beforeHost, afterHost)
		}
	}

	if retiredWorkers == 0 {
		t.Fatal("restore failures did not retire any non-initial worker thread")
	}

	restoreUTSNamespace = originalRestore
	got, err := Hostname(targetPID)
	if err != nil {
		t.Fatalf("Hostname(%d) after restore hook reset error = %v", targetPID, err)
	}
	if got != utsNamespaceHelperHostname {
		t.Fatalf("Hostname(%d) after restore hook reset = %q, want %q", targetPID, got, utsNamespaceHelperHostname)
	}
}

func utsNamespaceCommand(t testing.TB) (*exec.Cmd, context.CancelFunc) {
	t.Helper()

	// Test contexts are canceled before cleanup; keep the target alive until
	// cleanup has closed its stdin and waited for a graceful exit.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostname$", "-test.v")
	// Bound pipe draining if a descendant keeps an inherited descriptor open.
	cmd.WaitDelay = time.Second

	return cmd, cancel
}

func startUTSNamespaceTarget(t testing.TB) int {
	t.Helper()

	cmd, cancel := utsNamespaceCommand(t)
	cmd.Args = append(cmd.Args, "-test.v=false")
	cmd.Env = append(os.Environ(), utsNamespaceModeEnv+"=target")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUTS}
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
			t.Skip("creating a UTS namespace requires CAP_SYS_ADMIN")
		}
		t.Fatalf("start UTS namespace helper: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		// Allow EOF shutdown and the race runtime exit delay before killing.
		timer := time.AfterFunc(5*time.Second, cancel)
		defer timer.Stop()
		if err := cmd.Wait(); err != nil && !t.Failed() {
			t.Errorf("UTS namespace helper: %v", err)
		}
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("UTS namespace helper produced no status: %v", scanner.Err())
	}
	status := strings.TrimSpace(scanner.Text())
	if status != "ready" {
		t.Fatalf("UTS namespace helper status = %q, want ready", status)
	}
	return cmd.Process.Pid
}

func runUTSNamespaceCallerTest(t *testing.T, targetPID int) {
	t.Helper()

	cmd, _ := utsNamespaceCommand(t)
	cmd.Env = append(
		os.Environ(),
		utsNamespaceModeEnv+"=caller",
		utsNamespaceTargetPIDEnv+"="+strconv.Itoa(targetPID),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUTS}

	output, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 77 {
			t.Skipf("caller helper skipped: %s", output)
		}
		if errors.Is(err, syscall.EPERM) {
			t.Skip("creating a UTS namespace requires CAP_SYS_ADMIN")
		}
		t.Fatalf("caller helper failed: %v\n%s", err, output)
	}
}

func runUTSNamespaceTarget() {
	if err := unix.Sethostname([]byte(utsNamespaceHelperHostname)); err != nil {
		fmt.Fprintf(os.Stderr, "set helper hostname: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func runUTSNamespaceCaller() {
	targetPID, err := strconv.Atoi(os.Getenv(utsNamespaceTargetPIDEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse target PID: %v\n", err)
		os.Exit(1)
	}

	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_NEWUTS); err != nil {
			done <- err
			return
		}
		// Do not unlock after unshare. This helper exits immediately; leaving
		// the private namespace on a runtime-reused thread would contaminate it.
		if err := unix.Sethostname([]byte(utsNamespaceCallerHostname)); err != nil {
			done <- err
			return
		}
		beforeNS, err := currentThreadUTSNamespace()
		if err != nil {
			done <- err
			return
		}
		got, err := Hostname(targetPID)
		if err != nil {
			done <- err
			return
		}
		afterNS, err := currentThreadUTSNamespace()
		if err != nil {
			done <- err
			return
		}
		afterHost, err := os.Hostname()
		if err != nil {
			done <- err
			return
		}
		if got != utsNamespaceHelperHostname {
			done <- fmt.Errorf("Hostname(%d) = %q, want %q", targetPID, got, utsNamespaceHelperHostname)
			return
		}
		if afterHost != utsNamespaceCallerHostname {
			done <- fmt.Errorf("caller hostname = %q, want %q", afterHost, utsNamespaceCallerHostname)
			return
		}
		if afterNS != beforeNS {
			done <- fmt.Errorf("caller namespace changed: %q -> %q", beforeNS, afterNS)
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

func currentThreadUTSNamespace() (string, error) {
	return os.Readlink(procfs.Path("thread-self", "ns/uts"))
}

func BenchmarkCommandLine(b *testing.B) {
	for _, size := range []int{32, 128, 1024 * 1024} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			root := setProcRoot(b)
			path := filepath.Join(root, "proc", "100", "cmdline")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, size), 0o600); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			for b.Loop() {
				if _, err := CommandLine(100); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkHostname(b *testing.B) {
	for _, name := range []string{"self", "target"} {
		b.Run(name, func(b *testing.B) {
			pid := os.Getpid()
			if name == "target" {
				pid = startUTSNamespaceTarget(b)
			}

			want, err := Hostname(pid)
			if errors.Is(err, syscall.EPERM) {
				b.Skip("setns requires CAP_SYS_ADMIN; run in a user and UTS namespace")
			}
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			for b.Loop() {
				got, err := Hostname(pid)
				if err != nil || got != want {
					b.Fatalf("Hostname(%d) = %q, %v; want %q, nil", pid, got, err, want)
				}
			}
		})
	}
}

func setProcRoot(t testing.TB) string {
	t.Helper()
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return tmpRoot
}
