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

package transport

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenUDSCreatesMissingSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolstream.sock")

	listener, err := ListenUDS(path)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("mode = %v, want Unix socket", info.Mode())
	}
}

func TestListenUDSReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolstream.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	listener, err := ListenUDS(path)
	if err != nil {
		t.Fatalf("ListenUDS with stale socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

func TestListenUDSRejectsActiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolstream.sock")
	listener, err := ListenUDS(path)
	if err != nil {
		t.Fatalf("first ListenUDS: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	if _, err := ListenUDS(path); err == nil {
		t.Fatal("second ListenUDS error = nil, want active listener error")
	} else if !strings.Contains(err.Error(), "active listener") {
		t.Fatalf("second ListenUDS error = %q, want active listener error", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("active listener was disrupted: %v", err)
	}
	_ = conn.Close()
}

func TestListenUDSRejectsNonSocketPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolstream.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatalf("create regular file: %v", err)
	}

	if _, err := ListenUDS(path); err == nil {
		t.Fatal("ListenUDS error = nil, want non-socket path error")
	} else if !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("ListenUDS error = %q, want non-socket path error", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read regular file: %v", err)
	}
	if string(got) != "keep" {
		t.Fatalf("regular file content = %q, want keep", got)
	}
}

func TestListenerClosePreservesReplacedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolstream.sock")
	listener, err := ListenUDS(path)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove socket path: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("create replacement path: %v", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement path: %v", err)
	}
	if string(data) != "replacement" {
		t.Fatalf("replacement content = %q, want replacement", data)
	}
}
