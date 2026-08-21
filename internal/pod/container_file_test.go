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

package pod

import (
	"os"
	"path/filepath"
	"testing"
)

const testContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestContainerInitPIDInDockerRoot(t *testing.T) {
	rootDir := t.TempDir()
	containerDir := filepath.Join(rootDir, "containers", testContainerID)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	config := []byte(`{"State":{"Pid":1234}}`)
	if err := os.WriteFile(filepath.Join(containerDir, "config.v2.json"), config, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	pid, err := containerInitPIDInDockerRoot(rootDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("containerInitPIDInDockerRoot() error = %v", err)
	}
	if pid != 1234 {
		t.Fatalf("containerInitPIDInDockerRoot() = %d, want 1234", pid)
	}
}

func TestContainerInitPIDInContainerdState(t *testing.T) {
	stateDir := t.TempDir()
	containerDir := filepath.Join(
		stateDir,
		"io.containerd.runtime.v2.task",
		"k8s.io",
		testContainerID,
	)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(containerDir, "init.pid"), []byte("5678\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	pid, err := containerInitPIDInContainerdState(stateDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("containerInitPIDInContainerdState() error = %v", err)
	}
	if pid != 5678 {
		t.Fatalf("containerInitPIDInContainerdState() = %d, want 5678", pid)
	}
}

func TestResolveContainerIDRejectsAmbiguousPrefix(t *testing.T) {
	parentDir := t.TempDir()
	for _, id := range []string{
		"0123456789abaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0123456789abbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if err := os.Mkdir(filepath.Join(parentDir, id), 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
	}

	_, err := resolveContainerID(parentDir, "0123456789ab")
	if err == nil {
		t.Fatal("resolveContainerID() returned nil error for ambiguous prefix")
	}
}
