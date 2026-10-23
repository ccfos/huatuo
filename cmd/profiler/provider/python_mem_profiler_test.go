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
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	payload := buildAttachPayload("memray.Tracker()", 0, nil, filepath.Join(t.TempDir(), "cancel"), 0)
	// No Memray installation: verify marker-driven cleanup permits a retry,
	// and an old callback cannot stop the new session.
	harness := `
import json
import os
import sys
import threading
import types
memray = types.ModuleType("memray")
counts = {"entered": 0, "exited": 0}
deactivated = threading.Event()
class Tracker:
    def __enter__(self):
        counts["entered"] += 1
    def __exit__(self, *args):
        counts["exited"] += 1
        deactivated.set()
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
with open(first["CANCEL_PATH"], "w"):
    pass
first["session"].cancel_if_requested()
if not deactivated.wait(2):
    raise RuntimeError("cancellation marker did not deactivate the tracker")
with first["session"].lock:
    if memray._last_tracker is not None or memray._huatuo_attach_owner is not None:
        raise RuntimeError("cancellation did not release session ownership")
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

func TestMemrayInjectionFailureRequestsCancellation(t *testing.T) {
	if os.Getenv("HUATUO_MEMRAY_INJECTION_HELPER") == "1" {
		// Receive the control payload before reporting an uncertain GDB failure.
		var port string
		for _, arg := range os.Args {
			if value, ok := strings.CutPrefix(arg, "-ex=set $port="); ok {
				port = value
			}
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("HUATUO_MEMRAY_TEST_PAYLOAD"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		os.Exit(1)
	}

	work := t.TempDir()
	commands := filepath.Join(work, "memray", "commands")
	if err := os.MkdirAll(commands, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commands, "_attach.gdb"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec \"$HUATUO_MEMRAY_TEST_BINARY\" '-test.run=^TestMemrayInjectionFailureRequestsCancellation$' -- \"$@\"\n"
	gdbPath := filepath.Join(work, "gdb")
	if err := os.WriteFile(gdbPath, []byte(wrapper), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gdbPath, 0o700); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(work, "payload.py")
	t.Setenv("PATH", work+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUATUO_MEMRAY_INJECTION_HELPER", "1")
	t.Setenv("HUATUO_MEMRAY_TEST_BINARY", testBinary)
	t.Setenv("HUATUO_MEMRAY_TEST_PAYLOAD", payloadPath)
	identity, err := captureMemrayProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	profiler := &pythonMemoryProfiler{config: Config{Duration: time.Second}}
	err = profiler.runMemraySession(identity, ctx, work, "/unused/_inject.so", work, "bytes", func([]string) error {
		t.Error("failed injection emitted samples")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "run GDB injector") || ctx.Err() != nil {
		t.Fatalf("session error = %v, context error = %v, want GDB failure before cancellation", err, ctx.Err())
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var cancelPath string
	for _, line := range strings.Split(string(payload), "\n") {
		if value, ok := strings.CutPrefix(line, "CANCEL_PATH = "); ok {
			if err := json.Unmarshal([]byte(value), &cancelPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.HasPrefix(cancelPath, fmt.Sprintf("/tmp/.huatuo-memray-cancel-%d-", identity.pid)) {
		t.Fatalf("unexpected session cancellation path %q", cancelPath)
	}
	t.Cleanup(func() { _ = os.Remove(cancelPath) })
	if _, err := os.Stat(cancelPath); err != nil {
		t.Fatalf("uncertain injection did not request target cancellation: %v", err)
	}
}

func TestMemrayControlConnectionOwnership(t *testing.T) {
	if address := os.Getenv("HUATUO_MEMRAY_CONTROL_HELPER_ADDRESS"); address != "" {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(conn)
		if err != nil || len(payload) != 0 {
			t.Fatalf("foreign control peer received %q, %v; want close without payload", payload, err)
		}
		return
	}

	identity, err := captureMemrayProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopClose()
	deadline, _ := ctx.Deadline()
	const payload = "target payload"
	serverErr := make(chan error, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := identity.acceptControlConnection(listener)
		if err == nil {
			defer conn.Close()
			err = conn.SetWriteDeadline(deadline)
			if err == nil {
				_, err = io.WriteString(conn, payload)
			}
		}
		serverErr <- err
	}()
	defer func() {
		_ = listener.Close()
		<-serverDone
	}()

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A different PID in the same netns must be rejected before the real
	// target connects, without receiving any bytes or consuming the session.
	command := exec.CommandContext(ctx, testBinary, "-test.run=^TestMemrayControlConnectionOwnership$")
	command.Env = append(os.Environ(), "HUATUO_MEMRAY_CONTROL_HELPER_ADDRESS="+listener.Addr().String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("foreign control peer: %v\n%s", err, output)
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != payload {
		t.Fatalf("target control peer received %q, %v; want %q", got, err, payload)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestMemraySocketOwnership(t *testing.T) {
	if os.Getenv("HUATUO_MEMRAY_SOCKET_HELPER") == "1" {
		listener, err := net.Listen("tcp", os.Getenv("HUATUO_MEMRAY_SOCKET_ADDRESS"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		fmt.Println(listener.Addr())
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		claimedPID, err := strconv.ParseInt(os.Getenv("HUATUO_MEMRAY_SOCKET_PID"), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		// Send a complete stream claiming the victim's PID, not our own.
		header := binary.LittleEndian.AppendUint32([]byte("memray\x00"), 12)
		header = binary.LittleEndian.AppendUint32(header, 0)
		header = append(header, make([]byte, 35)...)
		header = binary.LittleEndian.AppendUint32(header, uint32(claimedPID))
		header = append(header, make([]byte, 19)...)
		header = append(header, 1) // trailer
		if _, err := conn.Write(header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, conn); err != nil {
			t.Fatal(err)
		}
		return
	}

	identity, err := captureMemrayProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	if owned, err := identity.ownsSocket(address, nil); !owned || err != nil {
		t.Fatalf("target listener ownership = %t, %v", owned, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, testBinary, "-test.run=^TestMemraySocketOwnership$")
	command.Env = append(os.Environ(), "HUATUO_MEMRAY_SOCKET_HELPER=1",
		"HUATUO_MEMRAY_SOCKET_ADDRESS="+address.String(),
		fmt.Sprintf("HUATUO_MEMRAY_SOCKET_PID=%d", identity.namespacePID))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	boundAddress, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(boundAddress) != address.String() {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("replacement listener = %q, %v; stderr: %s", boundAddress, err, stderr.String())
	}
	if owned, err := identity.ownsSocket(address, nil); owned || err != nil {
		t.Fatalf("foreign listener ownership = %t, %v", owned, err)
	}
	peer, err := captureMemrayProcessIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.close()
	conn, err := net.DialTimeout("tcp", address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := peer.validateDataConnection(ctx, conn); err != nil {
		t.Fatalf("accepted socket was not recognized: %v", err)
	}
	foreignCtx, cancelForeign := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelForeign()
	if err := identity.validateDataConnection(foreignCtx, conn); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("foreign data connection error = %v, want ownership rejection", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// PID checks alone accept this forgery; production checks ownership first.
	decoder, header, err := memray.NewStreamDecoder(conn, memray.Options{})
	if err != nil {
		t.Fatalf("foreign stream fixture is invalid: %v", err)
	}
	if err := identity.validateStreamHeader(header); err != nil {
		t.Fatalf("spoofed header did not claim the expected PID: %v", err)
	}
	if ok, err := decoder.NextRecord(); ok || err != nil {
		t.Fatalf("foreign stream trailer = %t, %v", ok, err)
	}
}

func TestMemrayStreamHeaderUsesNamespacePID(t *testing.T) {
	identity, err := captureMemrayProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.close()
	for _, test := range []struct {
		name         string
		namespacePID uint64
		headerPID    int32
		wantError    bool
	}{
		{"current target", identity.namespacePID, int32(identity.namespacePID), false},
		{"nested namespace", uint64(identity.pid) + 1, int32(identity.pid) + 1, false},
		{"host PID is not namespace PID", uint64(identity.pid) + 1, int32(identity.pid), true},
		{"zero PID", identity.namespacePID, 0, true},
		{"negative PID", identity.namespacePID, -1, true},
		{"missing identity PID", 0, int32(identity.pid), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := &memrayProcessIdentity{
				pid: identity.pid, namespacePID: test.namespacePID, startTime: identity.startTime, pidfd: -1,
			}
			err := target.validateStreamHeader(memray.Header{Pid: test.headerPID})
			if (err != nil) != test.wantError {
				t.Fatalf("header PID %d, namespace PID %d: %v", test.headerPID, test.namespacePID, err)
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

func TestPythonMemoryFinalize(t *testing.T) {
	profiler := &pythonMemoryProfiler{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	enqueue := func(*pipeline.SampleBatch) error {
		t.Error("Finalize emitted samples after the session drain")
		return nil
	}
	if err := profiler.Finalize(ctx, enqueue); err != nil {
		t.Fatalf("Finalize() error = %v, want nil", err)
	}
	cancel()
	if err := profiler.Finalize(ctx, enqueue); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finalize() error = %v, want cancellation", err)
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
