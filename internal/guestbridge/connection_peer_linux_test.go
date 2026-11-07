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

//go:build linux

package guestbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketListenerCloseRemovesOwnSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.hvsock_19705")
	listener, err := listenFirecracker(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still present after close: %v", err)
	}
}

func TestSocketListenerClosePreservesReplacedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.hvsock_19705")
	first, err := listenFirecracker(path)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}

	// Another process takes over the path while the first handler is draining.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove first socket: %v", err)
	}
	second, err := listenFirecracker(path)
	if err != nil {
		t.Fatalf("second listen: %v", err)
	}
	defer second.Close()

	secondListener, ok := second.(*socketListener)
	if !ok {
		t.Fatalf("unexpected listener type %T", second)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first listener: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("replacement socket was removed: %v", err)
	}
	if !os.SameFile(secondListener.fileInfo, info) {
		t.Fatal("path no longer refers to the replacement socket")
	}
}
