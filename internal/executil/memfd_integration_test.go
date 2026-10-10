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

package executil

import (
	"errors"
	"os"
	"testing"
)

func TestProcessStopPreservesMemfdUntilClose(t *testing.T) {
	process := startReadyProcess(t, `printf profile > "$1"; printf ready; while :; do sleep 30; done`,
		WithMemfdOutput(64, func(path string) []string { return []string{"memfd-test", path} }))
	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		data, err := process.MemfdOutput()
		if err != nil || string(data) != "profile" {
			t.Fatalf("MemfdOutput() = (%q,%v)", data, err)
		}
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := process.MemfdOutput(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("MemfdOutput(): %v", err)
	}
}

func TestProcessMemfdStartFailureClosesFile(t *testing.T) {
	for _, invalidArgs := range []bool{false, true} {
		name := "missing command"
		if invalidArgs {
			name = "invalid arguments"
		}
		t.Run(name, func(t *testing.T) {
			process, err := New(Spec{Path: "/missing/executil-command"}, WithMemfdOutput(64, func(string) []string {
				if invalidArgs {
					return []string{"bad\x00argument"}
				}
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Start(t.Context()); err == nil {
				t.Fatal("Start succeeded")
			}
			if _, err := process.MemfdOutput(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("MemfdOutput(): %v", err)
			}
			if err := process.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessDoesNotCloseBorrowedFiles(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "inherited")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	process, err := New(Spec{Path: "/bin/sh", Args: []string{"-c", `printf inherited >&3; printf stdout`}}, WithExtraFiles(file), WithStdout(file))
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("owned"); err != nil {
		t.Fatalf("borrowed file was closed: %v", err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || string(data) != "inheritedstdoutowned" {
		t.Fatalf("file = (%q,%v)", data, err)
	}
}
