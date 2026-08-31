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
	"net"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler/memray"
)

// TODO: Add a bounded packaged-runtime integration test that exercises a real
// Python process from GDB attachment through allocation and free decoding.

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
		"make_tracker()", 5, []string{"/tmp/memray"}, "/tmp/cancel",
	)
	for _, want := range []string{
		`PYTHON_PATHS = ["/tmp/memray"]`,
		"tracker = make_tracker()",
		`mode = "FOR_DURATION"`,
		`CANCEL_PATH = "/tmp/cancel"`,
		"start_cancel_watcher()",
		"track_for_duration(5)",
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("attach payload does not contain %q", want)
		}
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
	err := profiler.ReadDataLoop(context.Background(), func(any) {})
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("ReadDataLoop() error = %v, want not-started error", err)
	}
}

func TestPythonMemoryProfilerKeepsHostDuration(t *testing.T) {
	profiler := any(&pythonMemoryProfiler{})
	if _, managesDuration := profiler.(interface{ ManagesDuration() }); managesDuration {
		t.Fatal("Python memory profiler disables the registry duration timer")
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
