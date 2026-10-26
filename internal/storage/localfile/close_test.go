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
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestCloseReleasesTracerFiles(t *testing.T) {
	dir := t.TempDir()
	b := NewBackend(dir, 1, 2)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	for _, name := range []string{"cpu", "memory"} {
		if err := b.Save(t.Context(), driver.Record{Data: []byte(`{}`), Fields: map[string]any{"tracer_name": name}}, driver.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
			if err == nil && filepath.Dir(target) == dir {
				n++
			}
		}
		return n
	}
	if got := count(); got != 2 {
		t.Fatalf("open tracer files = %d, want 2", got)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Errorf("open tracer files after Close = %d, want 0", got)
	}
	for _, name := range []string{"cpu", "new-tracer"} {
		err := b.Save(t.Context(), driver.Record{Data: []byte(`{}`), Fields: map[string]any{"tracer_name": name}}, driver.SaveOptions{})
		if !errors.Is(err, fs.ErrClosed) {
			t.Errorf("Save after Close = %v, want ErrClosed", err)
		}
	}
	if got := count(); got != 0 {
		t.Errorf("Save reopened %d files after Close", got)
	}
}

type closeTestWriter struct {
	calls   int
	err     error
	entered chan struct{}
	release chan struct{}
}

func (w *closeTestWriter) Write(p []byte) (int, error) {
	if w.entered != nil {
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}
func (w *closeTestWriter) Close() error { w.calls++; return w.err }

var _ io.WriteCloser = (*closeTestWriter)(nil)

func TestCloseAllWritersAndRetainErrors(t *testing.T) {
	b := NewBackend(t.TempDir(), 1, 2)
	first := &closeTestWriter{err: errors.New("first close")}
	second := &closeTestWriter{err: errors.New("second close")}
	b.writerCache.Store("first", first)
	b.writerCache.Store("second", second)
	for range 2 {
		err := b.Close(t.Context())
		if !errors.Is(err, first.err) || !errors.Is(err, second.err) {
			t.Errorf("Close = %v, want both errors", err)
		}
	}
	if first.calls != 1 || second.calls != 1 {
		t.Errorf("close counts = %d, %d", first.calls, second.calls)
	}
}

func TestCloseJoinsInFlightSave(t *testing.T) {
	b := NewBackend(t.TempDir(), 1, 2)
	w := &closeTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	b.files["cpu"] = w
	b.writerCache.Store("cpu", w)
	saved := make(chan error, 1)
	go func() {
		saved <- b.Save(t.Context(), driver.Record{Data: []byte(`{}`), Fields: map[string]any{"tracer_name": "cpu"}}, driver.SaveOptions{})
	}()
	<-w.entered
	closed := make(chan error, 1)
	go func() { closed <- b.Close(t.Context()) }()
	select {
	case err := <-closed:
		close(w.release)
		<-saved
		t.Fatalf("Close returned before the active Write completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(w.release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Errorf("close calls = %d, want 1", w.calls)
	}
}
