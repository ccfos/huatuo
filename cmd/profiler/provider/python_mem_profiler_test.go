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

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiler/memray"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func TestAdaptMemrayInjectorScript(t *testing.T) {
	original := `call (void*)dlopen($libpath, $rtld_now)
eval "sharedlibrary %s", $libpath
p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"`

	got := adaptMemrayInjectorScript(original, "memray_schedule_client_direct")
	for _, want := range []string{
		"dlopen($target_libpath, $rtld_now)",
		`dlsym($handle, "memray_schedule_client_direct")`,
		"$fn == 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("adapted script does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sharedlibrary") {
		t.Fatalf("adapted script still loads symbols by path:\n%s", got)
	}
}

func TestBuildAttachPayloadForDuration(t *testing.T) {
	payload := buildAttachPayload(
		"make_tracker()", 5, []string{"/tmp/memray"}, "/tmp/cancel", 12345,
	)
	for _, want := range []string{
		`PYTHON_PATHS = ["/tmp/memray"]`,
		"tracker = make_tracker()",
		`CANCEL_PATH = "/tmp/cancel"`,
		"LIVE_PORT = 12345",
		"session.activate(5)",
		`if getattr(memray, "_huatuo_attach_owner", None) is not self:`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("attach payload does not contain %q", want)
		}
	}
}

func TestAttachPayloadSessionOwnership(t *testing.T) {
	_, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to evaluate the attach payload")
	}
	payload := buildAttachPayload("memray.Tracker()", 0, nil, "", 0)
	// No Memray installation: count tracker lifetime operations and
	// call an old session's callback after a new session has taken ownership.
	harness := `
import json
import sys
import types
memray = types.ModuleType("memray")
counts = {"entered": 0, "exited": 0}
class Tracker:
    def __enter__(self):
        counts["entered"] += 1
    def __exit__(self, *args):
        counts["exited"] += 1
memray.Tracker = Tracker
sys.modules["memray"] = memray
payload = ` + pythonStringLiteral(payload) + `
first = {}
exec(payload, first)
old_callback = first["session"].deactivate
duplicate_error = ""
try:
    exec(payload, {})
except Exception as error:
    duplicate_error = str(error)
first["session"].deactivate()
second = {}
exec(payload, second)
old_callback()
print(json.dumps({
    "entered": counts["entered"], "exited": counts["exited"],
    "duplicate_error": duplicate_error,
    "current_survives": memray._last_tracker is second["session"].tracker
        and memray._huatuo_attach_owner is second["session"],
}))
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", "-c", harness).Output()
	if err != nil {
		t.Fatalf("evaluate attach payload: %v", err)
	}
	var result struct {
		Entered         int    `json:"entered"`
		Exited          int    `json:"exited"`
		DuplicateError  string `json:"duplicate_error"`
		CurrentSurvives bool   `json:"current_survives"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode payload result %q: %v", output, err)
	}
	if result.Entered != 2 || result.Exited != 1 || !result.CurrentSurvives ||
		!strings.Contains(result.DuplicateError, "Memray session already active") {
		t.Fatalf("unexpected tracker ownership: %+v", result)
	}
}

func TestAttachPayloadCancelsPendingConnection(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to evaluate the attach payload")
	}
	for _, reason := range []string{"cancel", "timeout", "constructor failure"} {
		t.Run(reason, func(t *testing.T) {
			payload := buildAttachPayload("memray.Tracker()", 0, nil, filepath.Join(t.TempDir(), "cancel"), 0)
			// Match SocketDestination's blocking constructor, not __enter__.
			// The Python subprocess deadline also bounds a leaked activation lock.
			harness := `
import os
import socket
import sys
import threading
import types

memray = types.ModuleType("memray")
sys.modules["memray"] = memray
payload = ` + pythonStringLiteral(payload) + `
reason = ` + pythonStringLiteral(reason) + `
env = {}
exec(payload.rsplit("\nsession.activate(", 1)[0], env)
env["CONNECTION_TIMEOUT"] = 0.15 if reason == "timeout" else 10
counts = {"entered": 0, "exited": 0}
errors = []
listening = threading.Event()
constructor_finished = threading.Event()
constructor_failed = False
port = 0

class Tracker:
    def __init__(self):
        global constructor_failed, port
        if reason == "constructor failure" and not constructor_failed:
            constructor_failed = True
            raise RuntimeError("constructor failed")
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", port))
            listener.listen(1)
            port = listener.getsockname()[1]
            env["LIVE_PORT"] = port
            listening.set()
            connection, _ = listener.accept()
            connection.close()
        constructor_finished.set()

    def __enter__(self):
        counts["entered"] += 1

    def __exit__(self, *args):
        counts["exited"] += 1

memray.Tracker = Tracker
baseline_threads = set(threading.enumerate())
def activate():
    try:
        env["session"].activate(0)
    except Exception as error:
        errors.append(str(error))

activation = threading.Thread(target=activate, daemon=True)
activation.start()
if reason != "constructor failure":
    assert listening.wait(2), "constructor did not reach accept"
if reason == "cancel":
    # This must fail immediately, not wait for the starting session's lock.
    try:
        env["AttachSession"]().activate(0)
    except Exception as error:
        assert "already active" in str(error), str(error)
    else:
        raise AssertionError("duplicate session was accepted during startup")
    with open(env["CANCEL_PATH"], "w"):
        pass

activation.join(2)
assert not activation.is_alive(), "canceled constructor remained blocked"
expected_error = "constructor failed" if reason == "constructor failure" else "canceled or timed out"
assert len(errors) == 1 and expected_error in errors[0], errors
assert counts == {"entered": 0, "exited": 0}, counts
assert getattr(memray, "_huatuo_attach_owner", None) is None, "startup retained ownership"
assert set(threading.enumerate()) == baseline_threads, "startup watcher leaked"
if reason != "constructor failure":
    assert constructor_finished.is_set(), "constructor was not released"

# Reuse the same port and lock for a real connection after failed startup.
listening.clear()
constructor_finished.clear()
errors.clear()
env["CONNECTION_TIMEOUT"] = 10
env["session"].cancel_path = ""
activation = threading.Thread(target=activate, daemon=True)
activation.start()
assert listening.wait(2), "next session did not bind after failed startup"
with socket.create_connection(("127.0.0.1", port), timeout=1):
    pass
activation.join(2)
assert not activation.is_alive() and not errors, errors
assert constructor_finished.is_set()
env["session"].deactivate()
assert counts == {"entered": 1, "exited": 1}, counts
assert set(threading.enumerate()) == baseline_threads, "completed session leaked a thread"
`
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, "python3", "-c", harness).CombinedOutput(); err != nil {
				t.Fatalf("evaluate startup cancellation: %v\n%s", err, output)
			}
		})
	}
}

func TestMemraySessionOwnership(t *testing.T) {
	identity, err := captureMemrayProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.close()
	first, err := identity.lockSession()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := identity.lockSession()
	if err == nil {
		second.Close()
		t.Fatal("second session acquired an active target")
	}
	if !strings.Contains(err.Error(), "session already active") {
		t.Fatalf("unexpected lock failure: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := identity.lockSession()
	if err != nil {
		t.Fatalf("completed session did not release ownership: %v", err)
	}
	defer next.Close()

	stale := &memrayProcessIdentity{pid: identity.pid, startTime: identity.startTime + 1, pidfd: -1}
	if lock, err := stale.lockSession(); err == nil {
		lock.Close()
		t.Fatal("stale process identity acquired ownership")
	}
}

func TestPythonPathsLiteral(t *testing.T) {
	if got := pythonPathsLiteral(nil); got != "[]" {
		t.Fatalf("pythonPathsLiteral(nil) = %q, want []", got)
	}
	if got := pythonPathsLiteral([]string{`/tmp/a'b`}); got != `["/tmp/a'b"]` {
		t.Fatalf("escaped path literal = %q", got)
	}
}

func TestPythonMemoryReadBeforeStart(t *testing.T) {
	profiler := &pythonMemoryProfiler{}
	err := profiler.ReadDataLoop(context.Background(), func(*pipeline.SampleBatch) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("ReadDataLoop() error = %v, want not-started error", err)
	}
}

func TestPythonMemoryRejectsUnstartedCapture(t *testing.T) {
	for _, cause := range []error{profile.ErrCollectionDurationElapsed, profile.ErrStopRequested} {
		t.Run(cause.Error(), func(t *testing.T) {
			identity, err := captureMemrayProcessIdentity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			defer identity.close()
			profiler := &pythonMemoryProfiler{targets: []pythonMemoryTarget{{pid: os.Getpid(), identity: identity}}}
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)
			err = profiler.ReadDataLoop(ctx, func(*pipeline.SampleBatch) error {
				t.Error("unstarted capture emitted samples")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "incomplete Memray capture") {
				t.Fatalf("unstarted capture error = %v, want incomplete capture", err)
			}
		})
	}
}

func TestMemrayHeaderReadStopsOnContextCancellation(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stopContextClose := closeMemrayConnectionOnContextDone(ctx, reader)
	defer stopContextClose()

	errCh := make(chan error, 1)
	go func() {
		_, _, err := memray.NewStreamDecoder(reader, memray.Options{})
		errCh <- err
	}()
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("NewStreamDecoder() returned nil error after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("NewStreamDecoder() remained blocked after cancellation")
	}
}

func TestMemrayStreamReadStopsAfterCancellationGrace(t *testing.T) {
	for _, cause := range []error{profile.ErrCollectionDurationElapsed, profile.ErrStopRequested} {
		t.Run(cause.Error(), func(t *testing.T) {
			writer, reader := net.Pipe()
			defer writer.Close()
			defer reader.Close()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			stop := closeMemrayStreamOnContextDone(ctx, reader, 20*time.Millisecond)
			defer stop()

			readDone := make(chan struct{})
			readErr := make(chan error, 1)
			go func() {
				defer close(readDone)
				var record [2]byte
				_, err := io.ReadFull(reader, record[:])
				readErr <- err
			}()
			t.Cleanup(func() {
				_ = reader.Close()
				<-readDone
			})
			if err := writer.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			// No flush loop is running to notice cancellation or close the socket.
			cancel(cause)
			select {
			case err := <-readErr:
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("partial read error = %v, want closed pipe", err)
				}
			case <-time.After(time.Second):
				t.Fatal("partial read remained blocked after cancellation grace")
			}
		})
	}
}

func TestMemrayStreamWatchdogStopsWithSession(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := closeMemrayStreamOnContextDone(ctx, reader, 0)
	// Normal session completion must join the watcher without waiting for ctx.
	stop()
	cancel()
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("completed session's watcher closed the connection: %v", err)
	}
}

func TestPythonMemoryStartRejectsTooManyTargets(t *testing.T) {
	// Start only checks that GDB exists before validating targets; never run it.
	binDir := t.TempDir()
	if err := os.Symlink("/proc/self/exe", filepath.Join(binDir, "gdb")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	profiler := &pythonMemoryProfiler{config: Config{
		PIDs:                 []int{1, 2},
		Duration:             time.Second,
		MaxProfilerProcesses: 1,
	}}
	err := profiler.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "maximum=1, required=2") {
		t.Fatalf("Start() error = %v, want target-count rejection before attach", err)
	}
	if len(profiler.targets) != 0 {
		t.Fatalf("Start() prepared %d targets before rejecting the request", len(profiler.targets))
	}
}

func TestParsePythonMemoryStackMode(t *testing.T) {
	tests := []struct {
		value string
		want  memray.StackMode
	}{
		{"", memray.StackModePython},
		{"python", memray.StackModePython},
		{"hybrid", memray.StackModeHybrid},
		{"native", memray.StackModeNative},
	}
	for _, test := range tests {
		got, err := parsePythonMemoryStackMode(test.value)
		if err != nil {
			t.Fatalf("parsePythonMemoryStackMode(%q): %v", test.value, err)
		}
		if got != test.want {
			t.Fatalf("parsePythonMemoryStackMode(%q) = %d, want %d", test.value, got, test.want)
		}
	}

	if _, err := parsePythonMemoryStackMode("mixed"); err == nil {
		t.Fatal("parsePythonMemoryStackMode(mixed) returned nil error")
	}
}
