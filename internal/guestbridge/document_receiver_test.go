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
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/storage/vsock"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestAttributionRejectsForeignContainer(t *testing.T) {
	container := &pod.Container{
		ID:          "orders",
		SandboxID:   "orders-vm",
		RuntimeType: "io.containerd.kata-qemu.v2",
		Hostname:    "trusted-pod",
		Labels:      map[string]any{"HostNamespace": "application"},
	}
	receiver := &DocumentReceiver{
		Hostname:        "trusted-node",
		Region:          "trusted-region",
		LookupContainer: func(string) (*pod.Container, error) { return container, nil },
	}
	observed := timeutil.Now()
	original := types.Document{
		TracerID:          "guest-id",
		TracerName:        "cpu",
		TracerRunType:     types.TracerRunTypeEvent,
		ObservedTimestamp: &observed,
		ContainerID:       "orders",
		ContainerHostname: "forged",
		Hostname:          "forged",
		Region:            "forged",
	}
	for _, tc := range []struct {
		name, sandbox, namespace string
		valid                    bool
	}{
		{"same sandbox", "orders-vm", "application", true}, {
			"foreign sandbox",
			"payments-vm",
			"application",
			false,
		}, {"foreign namespace", "orders-vm", "system", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := original
			ctx := context.WithValue(
				t.Context(),
				peerIdentityKey{},
				PeerIdentity{SandboxID: tc.sandbox, Namespace: tc.namespace},
			)
			err := receiver.attribute(ctx, &d)
			if !tc.valid {
				if !errors.Is(err, ErrUntrustedPeer) {
					t.Errorf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Hostname != "trusted-node" ||
				d.Region != "trusted-region" ||
				d.ContainerHostname != "trusted-pod" ||
				d.SourceType != types.DocumentSourceKataGuest ||
				d.TracerID == original.TracerID ||
				d.ObservedTimestamp != original.ObservedTimestamp {
				t.Errorf("attribution=%+v", d)
			}
		})
	}
	if err := receiver.attribute(t.Context(), &original); !errors.Is(err, ErrUntrustedPeer) {
		t.Errorf("anonymous error=%v", err)
	}
	// VM-wide data must clear every forged container field and isolate its ID.
	first := original
	first.ContainerID = ""
	first.ContainerType = "forged"
	first.ContainerQoS = "forged"
	second := first
	if err := receiver.attribute(
		context.WithValue(t.Context(), peerIdentityKey{}, PeerIdentity{SandboxID: "orders-vm", Namespace: "application"}),
		&first,
	); err != nil {
		t.Fatal(err)
	}
	if err := receiver.attribute(
		context.WithValue(t.Context(), peerIdentityKey{}, PeerIdentity{SandboxID: "payments-vm", Namespace: "application"}),
		&second,
	); err != nil {
		t.Fatal(err)
	}
	if first.TracerID == second.TracerID ||
		first.ContainerHostname != "" ||
		first.ContainerType != "" ||
		first.ContainerQoS != "" {
		t.Errorf("VM attribution=%+v / %+v", first, second)
	}
}

func TestAuthenticatedStreamToLocalFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dir := t.TempDir()
	store, err := tracingstore.NewFromConfig(
		ctx,
		tracingstore.Config{LocalFile: &tracingstore.LocalFileConfig{Path: dir, RotationSizeMiB: 1, MaxRotatedFiles: 1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	events, unsubscribe := store.Subscribe()
	defer unsubscribe()
	receiver := &DocumentReceiver{Hostname: "trusted-node", Region: "trusted-region", Tracing: store}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var resolutions atomic.Int32
	bridge, err := New(Options{
		Receiver:        receiver,
		Registerer:      prometheus.NewRegistry(),
		FirecrackerRoot: t.TempDir(),
		QEMUListen: func(uint32) (net.Listener, error) {
			return listener, nil
		}, ResolvePeer: func(net.Addr, string) (PeerIdentity, error) {
			if resolutions.Add(1) == 1 {
				return PeerIdentity{}, errUnknownPeerSandbox
			}
			return PeerIdentity{SandboxID: "orders-vm", Namespace: "application"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := bridge.serve(listener, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer entry.server.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := transport.NewClientWithConnContext(
		ctx,
		conn,
		transport.ClientMetadata{ToolName: vsock.TracingTool, Version: "test", TaskID: ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := []byte(
		`{"hostname":"forged","region":"forged","tracer_id":"guest-event","tracer_name":"cpu",` +
			`"tracer_type":"event","observed_timestamp":"2026-10-16T01:00:00Z",` +
			`"tracer_data":{"counter":18446744073709551615}}`,
	)
	if err := client.SendChunk(raw, false); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-events:
		if d.Hostname != "trusted-node" || d.ContainerHostNamespace != "application" {
			t.Errorf("document=%+v", d)
		}
		if got := d.TracerData.(map[string]any)["counter"].(json.Number).String(); got != "18446744073709551615" {
			t.Errorf("counter=%s", got)
		}
	case <-ctx.Done():
		t.Fatal("document not received after discovery delay")
	}
	if err := entry.server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || len(data) == 0 {
		t.Fatalf("saved data=%q err=%v", data, err)
	}
	if err := receiver.Handle(ctx, "unknown", raw); !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("unknown tool=%v", err)
	}
	if err := receiver.Handle(ctx, vsock.TracingTool, append(raw, []byte(` {}`)...)); err == nil {
		t.Error("accepted trailing JSON")
	}
}

func TestFirecrackerCredentialAndSocketBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Error("removed regular file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Error("removed active listener")
	}
	_ = listener.Close()
}
