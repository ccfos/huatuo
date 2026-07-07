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

package localfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"huatuo-bamai/internal/storage/driver"
)

// TestBackendSave covers the localfile backend save behavior: verifies that fields.tracer_name is used as the filename and JSON content is pretty-printed before writing.
func TestBackendSave(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	err := backend.Save(t.Context(), driver.Record{
		ID:   "trace-20260424",
		Data: []byte("{\"tracer_name\":\"kernel_sched_tick\"}\n"),
		Fields: map[string]any{
			"tracer_name": "kernel_sched_tick",
		},
	})
	if err != nil {
		t.Errorf("Backend.Save() returned error: %v", err)
		return
	}

	data, err := os.ReadFile(filepath.Join(dir, "kernel_sched_tick"))
	if err != nil {
		t.Errorf("os.ReadFile() returned error: %v", err)
		return
	}

	want := "{\n\t\"tracer_name\": \"kernel_sched_tick\"\n}\n"
	if string(data) != want {
		t.Errorf("saved content = %q, want %q", string(data), want)
	}
}

// TestBackendSaveMkdirAllError verifies that Save returns an error when
// the storage directory cannot be created (e.g., permission denied).
// Before the fix, os.MkdirAll errors were silently discarded.
func TestBackendSaveMkdirAllError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}
	if os.Geteuid() == 0 {
		// root bypasses Unix permission checks, so a 0o555 parent still
		// allows MkdirAll and the test's premise no longer holds.
		t.Skip("permission-based test not reliable when running as root")
	}

	// Create a read-only parent directory so MkdirAll inside it will fail.
	parent := t.TempDir()
	readOnlyDir := filepath.Join(parent, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0o555); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Ensure cleanup can remove it even though it's read-only.
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o755) })

	// Use a subdirectory under the read-only dir that doesn't exist.
	// MkdirAll will fail because the parent is read-only.
	backend := NewBackend(filepath.Join(readOnlyDir, "nested", "data"), 1024, 3)

	err := backend.Save(t.Context(), driver.Record{
		ID:   "trace-permtest",
		Data: []byte("{\"test\": true}"),
		Fields: map[string]any{
			"tracer_name": "permtest",
		},
	})
	if err == nil {
		t.Errorf("Save() error=nil, want permission denied error")
	}
}

// TestBackendSaveInvalidJSONFallback verifies that when JSON formatting
// fails, Save falls back to writing raw data and logs a warning.
func TestBackendSaveInvalidJSONFallback(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	const tracerName = "badjson_test"
	want := []byte("not valid json {")

	err := backend.Save(t.Context(), driver.Record{
		ID:   "trace-badjson",
		Data: want,
		Fields: map[string]any{
			"tracer_name": tracerName,
		},
	})
	if err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, tracerName))
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v, want nil", tracerName, err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("saved content = %q, want %q", got, want)
	}
}

// TestBackendUnsupportedOperations covers operations not supported by the localfile backend: Get, Delete, Query, Count, and Terms all return ErrUnsupported.
func TestBackendUnsupportedOperations(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	if _, err := backend.Get(t.Context(), "trace-20260424"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Get() error = %v, want ErrUnsupported", err)
	}
	if err := backend.Delete(t.Context(), "trace-20260424"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Delete() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.Query(t.Context(), driver.Query{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Query() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.Count(t.Context(), driver.Query{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Count() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.Values(t.Context(), "tracer_name", driver.Query{}, 10); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Terms() error = %v, want ErrUnsupported", err)
	}
}
