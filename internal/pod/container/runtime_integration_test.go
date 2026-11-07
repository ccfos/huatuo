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

//go:build integration

package container

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func TestDockerRuntimeConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1.43/info" {
			t.Errorf("Docker request path=%q", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"DockerRootDir":"/var/lib/huatuo-test-docker"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	root, err := DockerRoot(t.Context(), "1.43")
	if err != nil || root != "/var/lib/huatuo-test-docker" {
		t.Errorf("Docker root=%q err=%v", root, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := DockerRoot(ctx, "1.43"); !errors.Is(err, context.Canceled) {
		t.Errorf("Docker cancellation=%v", err)
	}
}

type runtimeServerFixture struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	config   string
	canceled chan struct{}
}

func (s *runtimeServerFixture) Version(context.Context, *runtimeapi.VersionRequest) (*runtimeapi.VersionResponse, error) {
	return &runtimeapi.VersionResponse{Version: "0.1.0", RuntimeName: "containerd", RuntimeVersion: "1.7.0", RuntimeApiVersion: "v1"}, nil
}

func (s *runtimeServerFixture) Status(context.Context, *runtimeapi.StatusRequest) (*runtimeapi.StatusResponse, error) {
	return &runtimeapi.StatusResponse{
		Status: &runtimeapi.RuntimeStatus{Conditions: []*runtimeapi.RuntimeCondition{
			{Type: runtimeapi.RuntimeReady, Status: true}, {Type: runtimeapi.NetworkReady, Status: true},
		}},
		Info: map[string]string{"config": s.config},
	}, nil
}

func (s *runtimeServerFixture) ContainerStatus(ctx context.Context, request *runtimeapi.ContainerStatusRequest) (*runtimeapi.ContainerStatusResponse, error) {
	if request.ContainerId == "waiting" {
		<-ctx.Done()
		close(s.canceled)
		return nil, ctx.Err()
	}
	return &runtimeapi.ContainerStatusResponse{
		Status: &runtimeapi.ContainerStatus{
			Id: request.ContainerId, State: runtimeapi.ContainerState_CONTAINER_RUNNING,
			Metadata: &runtimeapi.ContainerMetadata{Name: "checkout"}, CreatedAt: 1,
			Image: &runtimeapi.ImageSpec{Image: "checkout"}, ImageRef: "checkout-image",
		},
		Info: map[string]string{"info": `{"pid":4321,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"checkout-vm"}`},
	}, nil
}

func runtimeSocketForTest(t *testing.T, fixture *runtimeServerFixture) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "cri.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(server, fixture)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("stop CRI fixture: %v", err)
		}
	})
	return "unix://" + socket
}

func TestContainerdRuntimeConnection(t *testing.T) {
	for _, test := range []struct {
		name, config string
		wantError    bool
	}{
		{name: "valid", config: `{"stateDir":"/run/containerd/io.containerd.grpc.v1.cri"}`},
		{name: "invalid config", config: "{", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := runtimeSocketForTest(t, &runtimeServerFixture{config: test.config})
			client, state, err := ConnectContainerd(t.Context(), endpoint)
			if test.wantError {
				if err == nil || client != nil || state != "" {
					t.Errorf("failed connection published state: client=%v state=%q err=%v", client, state, err)
				}
				return
			}
			if err != nil || state != "/run/containerd" {
				t.Errorf("containerd state=%q err=%v", state, err)
				return
			}
			info, err := ReadContainerdRuntimeInfo(t.Context(), client, "checkout")
			if err != nil || info.PID != 4321 || info.SandboxID != "checkout-vm" || info.RuntimeType != "io.containerd.kata-qemu.v2" {
				t.Errorf("runtime metadata=%+v err=%v", info, err)
			}
		})
	}
}

func TestContainerdRuntimeCancellation(t *testing.T) {
	fixture := &runtimeServerFixture{config: `{"stateDir":"/run/containerd/io.containerd.grpc.v1.cri"}`, canceled: make(chan struct{})}
	endpoint := runtimeSocketForTest(t, fixture)
	client, _, err := ConnectContainerd(t.Context(), endpoint)
	if err != nil {
		t.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := ReadContainerdRuntimeInfo(ctx, client, "waiting"); err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("CRI deadline=%v ctx=%v", err, ctx.Err())
	}
	select {
	case <-fixture.canceled:
	case <-time.After(time.Second):
		t.Error("CRI server did not receive cancellation")
	}
	if _, _, err := ConnectContainerd(ctx, endpoint); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("canceled initialization=%v", err)
	}
}
