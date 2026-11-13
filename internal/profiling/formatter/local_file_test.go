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

package formatter

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCreateOutputFileUsesUniqueNames(t *testing.T) {
	dir := t.TempDir()

	first, err := createOutputFile(dir, "perf", ".folded")
	if err != nil {
		t.Fatalf("create first output file: %v", err)
	}
	firstName := first.Name()
	if _, err := first.WriteString("first profile"); err != nil {
		t.Fatalf("write first output file: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first output file: %v", err)
	}

	second, err := createOutputFile(dir, "perf", ".folded")
	if err != nil {
		t.Fatalf("create second output file: %v", err)
	}
	secondName := second.Name()
	if err := second.Close(); err != nil {
		t.Fatalf("close second output file: %v", err)
	}

	if firstName == secondName {
		t.Fatalf("createOutputFile reused %q", firstName)
	}
	for _, name := range []string{firstName, secondName} {
		base := filepath.Base(name)
		if !strings.HasPrefix(base, "perf_") || !strings.HasSuffix(base, ".folded") {
			t.Errorf("output filename %q does not preserve prefix and extension", base)
		}
	}

	data, err := os.ReadFile(firstName)
	if err != nil {
		t.Fatalf("read first output file: %v", err)
	}
	if got, want := string(data), "first profile"; got != want {
		t.Errorf("first output content = %q, want %q", got, want)
	}
}

func TestLocalDestinationCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "profiles")

	destination, err := NewLocalDestination(Config{Format: FormatSVG, Destination: dir + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Open(); err != nil {
		t.Fatal(err)
	}
	file := destination.file

	if got := filepath.Dir(file.Name()); got != dir {
		t.Errorf("output directory = %q, want %q", got, dir)
	}
	if base := filepath.Base(file.Name()); !strings.HasPrefix(base, "flamegraph_") || !strings.HasSuffix(base, ".svg") {
		t.Errorf("output filename %q does not preserve prefix and extension", base)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}

func TestWriteAndClosePropagatesOutputFailures(t *testing.T) {
	writeFailure := errors.New("write failed")
	closeFailure := errors.New("close failed")
	for _, tc := range []struct {
		name               string
		writeErr, closeErr error
	}{
		{name: "success"},
		{name: "write", writeErr: writeFailure},
		{name: "close", closeErr: closeFailure},
		{name: "both", writeErr: writeFailure, closeErr: closeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &failingOutputFile{closeErr: tc.closeErr}
			err := writeAndClose(file, func(io.Writer) error { return tc.writeErr })
			if file.closes != 1 {
				t.Fatalf("Close called %d times", file.closes)
			}
			for _, failure := range []error{tc.writeErr, tc.closeErr} {
				if failure != nil && !errors.Is(err, failure) {
					t.Fatalf("output error %v does not include %v", err, failure)
				}
			}
			//nolint:errorlint // Successful cleanup preserves the write error unchanged.
			if tc.closeErr == nil && err != tc.writeErr {
				t.Fatalf("write error was wrapped: %v", err)
			}
		})
	}
}

type failingOutputFile struct {
	closeErr error
	closes   int
}

func (*failingOutputFile) Write(p []byte) (int, error) { return len(p), nil }
func (f *failingOutputFile) Close() error {
	f.closes++
	return f.closeErr
}

func TestLocalDestinationResolvesPaths(t *testing.T) {
	for _, tc := range []struct {
		name                string
		existing, directory bool
	}{
		{name: "existing file", existing: true},
		{name: "new file"},
		{name: "existing directory", existing: true, directory: true},
		{name: "new directory", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output")
			if tc.existing {
				if tc.directory {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("previous data"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if tc.directory {
				path += "/"
			}
			destination, err := NewLocalDestination(Config{Format: FormatCollapsed, Destination: path})
			if err != nil {
				t.Fatal(err)
			}
			if tc.existing && !tc.directory {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "previous data" {
					t.Fatalf("constructor changed file: %q, %v", data, err)
				}
			}
			if err := destination.WriteAndClose(func(w io.Writer) error { _, err := io.WriteString(w, "work 2\n"); return err }); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				files, err := filepath.Glob(filepath.Join(path, "perf_*.folded"))
				if err != nil || len(files) != 1 {
					t.Fatalf("output files = %v, error = %v", files, err)
				}
				path = files[0]
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "work 2\n" {
				t.Fatalf("output = %q, error = %v", data, err)
			}
		})
	}
}

func TestLocalDestinationRejectsSpecialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalDestination(Config{Destination: path}); err == nil {
		t.Fatal("accepted FIFO destination")
	}
}

func TestLocalDestinationRejectsChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	destination, err := NewLocalDestination(Config{Destination: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := destination.WriteAndClose(func(io.Writer) error { t.Fatal("wrote to FIFO"); return nil }); err == nil {
		t.Fatal("accepted replaced destination")
	}
}

func TestLocalDestinationReportsCloseFailureOnce(t *testing.T) {
	destination, err := NewLocalDestination(Config{Format: FormatJSON, Destination: filepath.Join(t.TempDir(), "profile")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Open(); err != nil {
		t.Fatal(err)
	}
	if err := destination.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := destination.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Close = %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("Close repeated failure: %v", err)
	}
}
