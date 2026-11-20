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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
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
	}, driver.SaveOptions{})
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

// TestBackendSaveSeparatesRecords covers the record boundary contract: the
// producers encode documents with json.Marshal, which emits no trailing
// newline, so the backend has to terminate every record or consecutive
// documents fuse into one unparseable stream.
func TestBackendSaveSeparatesRecords(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	ids := []string{"first", "second"}
	for _, id := range ids {
		data, err := json.Marshal(map[string]string{
			"tracer_name": "kernel_sched_tick",
			"tracer_id":   id,
		})
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		err = backend.Save(t.Context(), driver.Record{
			ID:     id,
			Data:   data,
			Fields: map[string]any{"tracer_name": "kernel_sched_tick"},
		}, driver.SaveOptions{})
		if err != nil {
			t.Fatalf("Backend.Save(%s) returned error: %v", id, err)
		}
	}

	content, err := os.ReadFile(filepath.Join(dir, "kernel_sched_tick"))
	if err != nil {
		t.Fatalf("os.ReadFile() returned error: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	for i, id := range ids {
		document := map[string]string{}
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("record %d is not decodable: %v, content %q", i, err, content)
		}
		if document["tracer_id"] != id {
			t.Errorf("record %d tracer_id = %q, want %q", i, document["tracer_id"], id)
		}

		// The next byte after a record must be the separator; without it the
		// following document starts at the closing brace of this one and
		// line-oriented consumers cannot split the file.
		if offset := decoder.InputOffset(); offset < int64(len(content)) && content[offset] != '\n' {
			t.Fatalf("record %d is not newline-separated: %q", i, content)
		}
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
	}, driver.SaveOptions{})
	if err == nil {
		t.Errorf("Save() error=nil, want permission denied error")
	}
}

// TestBackendSaveInvalidJSONFallback verifies that when JSON formatting
// fails, Save falls back to writing raw data, terminated like any other
// record, and logs a warning.
func TestBackendSaveInvalidJSONFallback(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	const tracerName = "badjson_test"
	raw := []byte("not valid json {")
	want := []byte("not valid json {\n")

	err := backend.Save(t.Context(), driver.Record{
		ID:   "trace-badjson",
		Data: raw,
		Fields: map[string]any{
			"tracer_name": tracerName,
		},
	}, driver.SaveOptions{})
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

// TestBackendUnsupportedOperations covers operations not supported by the localfile backend: Get, Delete, DeleteByQuery, Query, Count, and Terms all return ErrUnsupported.
func TestBackendUnsupportedOperations(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)

	if _, err := backend.Get(t.Context(), "trace-20260424"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Get() error = %v, want ErrUnsupported", err)
	}
	if err := backend.Delete(t.Context(), "trace-20260424"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Delete() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.DeleteByQuery() error = %v, want ErrUnsupported", err)
	}
	if err := backend.Query(t.Context(), driver.Query{Limit: 1}, func([]driver.Record) error { return nil }); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Query() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.Count(t.Context(), driver.Query{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Count() error = %v, want ErrUnsupported", err)
	}
	if _, err := backend.Values(t.Context(), "tracer_name", driver.Query{}, 10); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Backend.Terms() error = %v, want ErrUnsupported", err)
	}
}

// tracerRecord builds the minimal record the localfile backend needs: the
// tracer_name field selects the output file and the data is written verbatim.
func tracerRecord(name, id string) driver.Record {
	return driver.Record{
		ID:     id,
		Data:   []byte(fmt.Sprintf("{\"tracer_name\":%q}", name)),
		Fields: map[string]any{"tracer_name": name},
	}
}

// TestBackendSaveConcurrentTracers saves from many goroutines at once, each
// using its own tracer name, and checks that every name ends up in exactly one
// file holding all of its records.
//
// Regression test: writerByName used to read the writer map without holding
// the lock while the miss path inserted into that same map under the lock, so
// two concurrent Saves could abort the whole process with "concurrent map read
// and map write". Run with -race to catch a regression deterministically.
func TestBackendSaveConcurrentTracers(t *testing.T) {
	const (
		workers    = 16
		iterations = 4
	)

	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)
	t.Cleanup(func() { _ = backend.Close(t.Context()) })

	// Release every worker at the same instant so the first Save of each
	// tracer name reaches the cache-miss path concurrently.
	var (
		ready sync.WaitGroup
		done  sync.WaitGroup
	)
	start := make(chan struct{})
	ready.Add(workers)
	done.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			name := fmt.Sprintf("tracer-%d", i)
			ready.Done()
			<-start
			for j := 0; j < iterations; j++ {
				err := backend.Save(
					t.Context(),
					tracerRecord(name, fmt.Sprintf("%s-%d", name, j)),
					driver.SaveOptions{},
				)
				if err != nil {
					t.Errorf("Save(%q) = %v, want nil", name, err)
					return
				}
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) = %v, want nil", dir, err)
	}
	if len(entries) != workers {
		t.Errorf("files in %q = %d, want %d", dir, len(entries), workers)
	}

	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("tracer-%d", i)
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Errorf("ReadFile(%q) = %v, want nil", name, readErr)
			continue
		}
		want := strings.Repeat(fmt.Sprintf("{\n\t\"tracer_name\": %q\n}", name), iterations)
		if string(got) != want {
			t.Errorf("content of %q = %q, want %q", name, string(got), want)
		}
	}
}

// TestBackendSaveSameTracerConcurrently hammers one tracer name from many
// goroutines. It covers the cache-miss path where every goroutine contends for
// the write lock and only the first opens the rotator, and it checks that no
// record is lost while they all append to the shared file.
func TestBackendSaveSameTracerConcurrently(t *testing.T) {
	const (
		workers    = 8
		iterations = 16
	)

	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)
	t.Cleanup(func() { _ = backend.Close(t.Context()) })

	const name = "shared_tracer"

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				err := backend.Save(
					t.Context(),
					tracerRecord(name, fmt.Sprintf("%d-%d", i, j)),
					driver.SaveOptions{},
				)
				if err != nil {
					t.Errorf("Save(%q) = %v, want nil", name, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v, want nil", name, err)
	}
	if count := strings.Count(string(got), "\"tracer_name\""); count != workers*iterations {
		t.Errorf("records in %q = %d, want %d", name, count, workers*iterations)
	}
}

// TestBackendSaveReusesRotator verifies that repeated Saves for one tracer name
// reuse the cached rotator and append to the same file instead of truncating
// it or opening a second descriptor.
func TestBackendSaveReusesRotator(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)
	t.Cleanup(func() { _ = backend.Close(t.Context()) })

	const name = "reuse_tracer"

	first, err := backend.writerByName(name)
	if err != nil {
		t.Fatalf("writerByName(%q) = %v, want nil", name, err)
	}
	second, err := backend.writerByName(name)
	if err != nil {
		t.Fatalf("writerByName(%q) = %v, want nil", name, err)
	}
	if first != second {
		t.Errorf("writerByName(%q) returned a different writer on the second call", name)
	}

	const saves = 3
	for i := 0; i < saves; i++ {
		err := backend.Save(t.Context(), tracerRecord(name, fmt.Sprintf("%s-%d", name, i)), driver.SaveOptions{})
		if err != nil {
			t.Fatalf("Save(%q) = %v, want nil", name, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v, want nil", name, err)
	}
	want := strings.Repeat(fmt.Sprintf("{\n\t\"tracer_name\": %q\n}", name), saves)
	if string(got) != want {
		t.Errorf("content of %q = %q, want %q", name, string(got), want)
	}
}

// TestBackendSaveMissingTracerName covers the empty/missing-field path: without
// a tracer_name field no output file can be selected, so Save must report
// ErrInvalidField and must not create anything on disk.
func TestBackendSaveMissingTracerName(t *testing.T) {
	dir := t.TempDir()
	backend := NewBackend(dir, 1024, 3)
	t.Cleanup(func() { _ = backend.Close(t.Context()) })

	err := backend.Save(t.Context(), driver.Record{
		ID:   "missing-name",
		Data: []byte("{\"tracer_name\":\"\"}"),
	}, driver.SaveOptions{})
	if !errors.Is(err, driver.ErrInvalidField) {
		t.Errorf("Save() error = %v, want ErrInvalidField", err)
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir(%q) = %v, want nil", dir, readErr)
	}
	if len(entries) != 0 {
		t.Errorf("files in %q = %d, want 0", dir, len(entries))
	}
}
