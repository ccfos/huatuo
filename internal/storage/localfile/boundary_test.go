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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestInvalidFilenameHasNoFilesystemSideEffects(t *testing.T) {
	for _, name := range []string{
		"",
		".",
		"..",
		"../escape",
		"nested/trace",
		"/absolute",
		"nested\\trace",
		"bad\x00name",
	} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			backend := NewBackend(dir, 1, 2)
			err := backend.Save(
				t.Context(),
				driver.Record{Data: []byte(`{}`), Fields: map[string]any{"tracer_name": name}},
				driver.SaveOptions{},
			)
			if !errors.Is(err, driver.ErrInvalidField) {
				t.Errorf("filename=%q error=%v", name, err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("invalid name created storage directory: %v", err)
			}
		})
	}
}

func TestConcurrentTracerWriters(t *testing.T) {
	backend := NewBackend(t.TempDir(), 1, 2)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 4 {
				err := backend.Save(
					t.Context(),
					driver.Record{Data: []byte(`{}`), Fields: map[string]any{"tracer_name": fmt.Sprintf("tracer_%d", i)}},
					driver.SaveOptions{},
				)
				if err != nil {
					t.Errorf("concurrent Save: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
	for i := range 32 {
		if _, err := os.Stat(filepath.Join(backend.path, fmt.Sprintf("tracer_%d", i))); err != nil {
			t.Errorf("missing tracer output: %v", err)
		}
	}
}
