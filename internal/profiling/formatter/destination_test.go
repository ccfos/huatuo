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
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateDestination(t *testing.T) {
	for _, path := range []string{"", " "} {
		t.Run(path, func(t *testing.T) {
			if err := ValidateDestination(path); err == nil {
				t.Fatalf("accepted destination %q", path)
			}
		})
	}
	for _, path := range []string{"profile.svg", "profiles/", "/run/toolstream.sock", "stdout", "-", "/dev/stdout", "/dev/fd/1", "/proc/self/fd/1"} {
		if err := ValidateDestination(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateTUIDestination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name: "omitted path",
		},
		{
			name: "stdout",
			path: "stdout",
		},
		{
			name: "stdout alias",
			path: "-",
		},
		{
			name: "file path",
			path: "/tmp/result",
		},
		{
			name:    "blank path",
			path:    " ",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTUIDestination(tc.path); (err != nil) != tc.wantErr {
				t.Fatalf("ValidateTUIDestination(%q) = %v, want error: %t", tc.path, err, tc.wantErr)
			}
		})
	}
}

func TestSaveAtomicallyReplacesFileOnlyAfterWriteSucceeds(t *testing.T) {
	for _, name := range []string{"file", "symlink"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "profile.gob")
			require.NoError(t, os.WriteFile(path, []byte("previous"), 0o400))
			outputPath := path
			if name == "symlink" {
				outputPath = filepath.Join(t.TempDir(), "alias.gob")
				relativePath, err := filepath.Rel(filepath.Dir(outputPath), path)
				require.NoError(t, err)
				require.NoError(t, os.Symlink(relativePath, outputPath))
			}

			destination, err := NewLocalDestination(Config{Destination: outputPath, Format: FormatTUI})
			require.NoError(t, err)
			writeErr := errors.New("injected write failure")
			_, err = destination.SaveAtomically(t.Context(), ".gob", func(w io.Writer) error {
				_, err := io.WriteString(w, "partial")
				return errors.Join(err, writeErr)
			})
			require.ErrorIs(t, err, writeErr)
			content, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			require.Equal(t, "previous", string(content))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1)

			saved, err := destination.SaveAtomically(t.Context(), ".gob", func(w io.Writer) error {
				_, err := io.WriteString(w, "complete")
				return err
			})
			require.NoError(t, err)
			require.Equal(t, path, saved)
			content, err = os.ReadFile(outputPath)
			require.NoError(t, err)
			require.Equal(t, "complete", string(content))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o400), info.Mode().Perm())
			if name == "symlink" {
				link, err := os.Lstat(outputPath)
				require.NoError(t, err)
				require.NotZero(t, link.Mode()&os.ModeSymlink)
			}
		})
	}
}

func TestSaveAtomicallyCancelsWhileWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.gob")
	require.NoError(t, os.WriteFile(path, []byte("previous"), 0o600))
	destination, err := NewLocalDestination(Config{
		Destination: path,
		Format:      FormatTUI,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)

	saveDone := make(chan error, 1)
	go func() {
		_, err := destination.SaveAtomically(ctx, ".gob", func(w io.Writer) error {
			close(started)
			<-release
			_, err := io.WriteString(w, "late output")
			return err
		})
		saveDone <- err
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("report writer did not start")
	}
	select {
	case err := <-saveDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("report save waited for the blocked writer after cancellation")
	}

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "previous", string(content))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestTUIDestinationRejectsLinksToDirectoriesAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, linkedPath := range []string{dir, filepath.Join(dir, "missing.gob")} {
		path := filepath.Join(t.TempDir(), "alias.gob")
		require.NoError(t, os.Symlink(linkedPath, path))
		_, err := NewLocalDestination(Config{Destination: path, Format: FormatTUI})
		require.Error(t, err)
	}
}
