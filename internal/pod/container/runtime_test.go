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

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	criapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
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

	pid, err := ReadDockerPID(rootDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("ReadDockerPID() error = %v", err)
	}
	if pid != 1234 {
		t.Fatalf("ReadDockerPID() = %d, want 1234", pid)
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

	pid, err := ReadContainerdPID(stateDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("ReadContainerdPID() error = %v", err)
	}
	if pid != 5678 {
		t.Fatalf("ReadContainerdPID() = %d, want 5678", pid)
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

type pidRuntimeService struct {
	criapi.RuntimeService
	response     *runtimeapi.ContainerStatusResponse
	err          error
	calls        int
	containerID  string
	verbose      bool
	checkContext bool
}

func (s *pidRuntimeService) ContainerStatus(
	ctx context.Context,
	id string,
	verbose bool,
) (*runtimeapi.ContainerStatusResponse, error) {
	s.calls++
	s.containerID = id
	s.verbose = verbose
	if s.checkContext {
		return nil, ctx.Err()
	}
	return s.response, s.err
}

func runningRuntimeResponse(info string) *runtimeapi.ContainerStatusResponse {
	return &runtimeapi.ContainerStatusResponse{
		Status: &runtimeapi.ContainerStatus{Id: "test", State: runtimeapi.ContainerState_CONTAINER_RUNNING},
		Info:   map[string]string{"info": info},
	}
}

func TestReadContainerdRuntimeInfo(t *testing.T) {
	exited := runningRuntimeResponse(`{"runtimeType":"io.containerd.kata.v2"}`)
	exited.Status.State = runtimeapi.ContainerState_CONTAINER_EXITED
	wrongID := runningRuntimeResponse(`{"runtimeType":"io.containerd.kata.v2"}`)
	wrongID.Status.Id = "another"
	for _, tt := range []struct {
		name     string
		response *runtimeapi.ContainerStatusResponse
		err      error
		wantErr  string
	}{
		{
			name:     "Kata",
			response: runningRuntimeResponse(`{"pid":1234,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"sandbox"}`),
		},
		{
			name:     "runc",
			response: runningRuntimeResponse(`{"pid":1234,"runtimeType":"io.containerd.runc.v2","sandboxID":"sandbox"}`),
		},
		{name: "missing runtime", response: runningRuntimeResponse(`{"pid":1234}`), wantErr: "no CRI runtime type"},
		{name: "missing info", response: runningRuntimeResponse(""), wantErr: "decode containerd container info"},
		{name: "bad JSON", response: runningRuntimeResponse("invalid"), wantErr: "decode containerd container info"},
		{name: "exited", response: exited, wantErr: "no matching running CRI status"},
		{name: "wrong ID", response: wrongID, wantErr: "no matching running CRI status"},
		{name: "nil response", wantErr: "no matching running CRI status"},
		{name: "nil status", response: &runtimeapi.ContainerStatusResponse{}, wantErr: "no matching running CRI status"},
		{name: "CRI error", err: errors.New("runtime unavailable"), wantErr: "runtime unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &pidRuntimeService{response: tt.response, err: tt.err}
			info, err := ReadContainerdRuntimeInfo(t.Context(), client, "test")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			} else if info.PID != 1234 || info.SandboxID != "sandbox" || info.RuntimeType == "" {
				t.Errorf("runtime info = %+v", info)
			}
			if client.calls != 1 || client.containerID != "test" || !client.verbose {
				t.Errorf("unexpected CRI request: %+v", client)
			}
		})
	}
	if _, err := ReadContainerdRuntimeInfo(t.Context(), nil, "test"); err == nil {
		t.Error("uninitialized CRI client must fail")
	}
}
