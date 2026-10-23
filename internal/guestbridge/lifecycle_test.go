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

package guestbridge

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/storage/vsock"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestUnixCredentialBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	for _, pid := range []int{os.Getpid(), os.Getpid() + 1000000} {
		bridge := &Bridge{handshakeTimeout: time.Second, resolvePeer: func(net.Addr, string) (PeerIdentity, error) {
			return PeerIdentity{SandboxID: "orders-vm", Namespace: "application", HypervisorPID: pid}, nil
		}}
		_, err := bridge.waitConnectionPeer(t.Context(), accepted, "orders-vm")
		if pid == os.Getpid() && err != nil {
			t.Errorf("matching PID: %v", err)
		}
		if pid != os.Getpid() && !errors.Is(err, ErrUntrustedPeer) {
			t.Errorf("foreign PID: %v", err)
		}
	}
}

func TestShutdownWaitsBeforeStorageClose(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(Options{Registerer: prometheus.NewRegistry(), FirecrackerRoot: t.TempDir(), QEMUListen: func(
		uint32,
	) (net.Listener, error) {
		return listener, nil
	}, ResolvePeer: func(net.Addr, string) (PeerIdentity, error) {
		return PeerIdentity{SandboxID: "orders-vm", Namespace: "application"}, nil
	}, Receiver: &DocumentReceiver{LookupContainer: func(string) (*pod.Container, error) {
		close(entered)
		<-release
		return nil, errors.New("container removed")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := transport.NewClientWithConn(
		conn,
		transport.ClientMetadata{ServiceName: vsock.TracingTool, ToolName: vsock.TracingTool, Version: "test", TaskID: ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := []byte(
		`{"container_id":"orders","tracer_id":"guest-event","tracer_name":"cpu",` +
			`"tracer_type":"event","observed_timestamp":"2026-10-16T01:00:00Z"}`,
	)
	if err := client.Send(t.Context(), raw, transport.SendOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := bridge.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown=%v", err)
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if err := bridge.Shutdown(ctx2); err != nil {
		t.Fatal(err)
	}
}

func TestFirecrackerListenerLifecycle(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "orders-vm", "root", "kata.hvsock")
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatal(err)
	}
	baseListener, err := net.Listen("unix", base)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := New(
		Options{
			Receiver:            &DocumentReceiver{},
			Registerer:          prometheus.NewRegistry(),
			FirecrackerRoot:     root,
			FirecrackerInterval: 5 * time.Millisecond,
			QEMUListen:          func(uint32) (net.Listener, error) { return nil, errors.New("no qemu") },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	path := base + "_19705"
	waitSocket := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			_, err := os.Stat(path)
			if (err == nil) == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("listener existence did not become %v", want)
	}
	waitSocket(true)
	if err := baseListener.Close(); err != nil {
		t.Fatal(err)
	}
	waitSocket(false)
}

func TestLongFirecrackerSocketPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("a", 64), "root", "kata.hvsock_39765")
	if len(path) <= 108 {
		t.Fatal("fixture must exceed Unix address size")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := listenFirecracker(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Error("removed active long socket")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket cleanup=%v", err)
	}
}
