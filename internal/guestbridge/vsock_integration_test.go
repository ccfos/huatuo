//go:build integration && linux

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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/ccfos/huatuo/internal/pod"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
)

// Selected containers and the ready path are supplied by an isolated deployment
// script. This does not start the global Pod manager or BPF collectors.
func TestKataVSOCKIntegration(t *testing.T) {
	metadata := os.Getenv("HUATUO_KATA_BRIDGE_CONTAINERS")
	if metadata == "" {
		t.Skip("explicit Kata metadata snapshot required")
	}
	raw, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	containers := map[string]*pod.Container{}
	if err := json.Unmarshal(raw, &containers); err != nil {
		t.Fatal(err)
	}
	selected := containers[os.Getenv("HUATUO_KATA_BRIDGE_CONTAINER")]
	if selected == nil {
		t.Fatal("selected container missing")
	}
	port, err := strconv.ParseUint(os.Getenv("HUATUO_KATA_BRIDGE_PORT"), 10, 32)
	if err != nil || port < 1024 {
		t.Fatal("explicit nonprivileged port required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	traces, err := tracingstore.NewFromConfig(
		ctx,
		tracingstore.Config{
			LocalFile: &tracingstore.LocalFileConfig{Path: t.TempDir(), RotationSizeMiB: 1, MaxRotatedFiles: 1},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer traces.Close(context.Background())
	events, unsubscribe := traces.Subscribe()
	defer unsubscribe()
	profiles, records := newProfileFixture(t)
	bridge, err := New(Options{
		Receiver: &DocumentReceiver{
			Hostname: "verified-host", Region: "integration",
			Tracing: traces, Profiling: profiles,
			LookupContainer: func(id string) (*pod.Container, error) {
				c := containers[id]
				if c == nil {
					return nil, fmt.Errorf("unknown container")
				}
				return c, nil
			},
		}, Registerer: prometheus.NewRegistry(), ResolvePeer: func(addr net.Addr, sandbox string) (PeerIdentity, error) {
			return resolvePeerFromContainers(addr, sandbox, containers, os.ReadFile)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var listener net.Listener
	sandbox := ""
	if os.Getenv("HUATUO_KATA_BRIDGE_CHANNEL") == "firecracker" {
		sandbox = selected.SandboxID
		root := os.Getenv("HUATUO_KATA_BRIDGE_FC_ROOT")
		if root == "" {
			root = DefaultFirecrackerRoot
		}
		path := filepath.Join(root, sandbox, "root", fmt.Sprintf("kata.hvsock_%d", port))
		if err := removeStaleSocket(path); err != nil {
			t.Fatal(err)
		}
		listener, err = bridge.unixListen(path)
	} else {
		listener, err = listenQEMU(uint32(port))
	}
	if err != nil {
		t.Fatal(err)
	}
	entry, err := bridge.serve(listener, "real-kata", sandbox)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	defer entry.server.Close()
	ready := os.Getenv("HUATUO_KATA_BRIDGE_READY")
	if ready == "" {
		t.Fatal("ready path required")
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case document := <-events:
		if document.ContainerID != selected.ID || document.Hostname != "verified-host" {
			t.Fatalf("wrong event attribution: %+v", document)
		}
		value := document.TracerData.(map[string]any)["counter"].(json.Number).String()
		if value != "18446744073709551615" {
			t.Fatalf("counter=%s", value)
		}
	case <-ctx.Done():
		t.Fatal("real Kata event not received")
	}
	select {
	case raw := <-records:
		document, err := profilingstore.DecodeDocument(raw)
		if err != nil {
			t.Fatal(err)
		}
		if document.ContainerID != selected.ID || document.ProfileData.Profile.Sample[0].Value[0] != 9007199254740993 {
			t.Fatalf("profile=%+v", document)
		}
	case <-ctx.Done():
		t.Fatal("real Kata profile not received")
	}
	if err := entry.server.Close(); err != nil {
		t.Fatal(err)
	}
	metric := &dto.Metric{}
	if err := bridge.metrics.dropped.WithLabelValues("untrusted_peer").Write(metric); err != nil {
		t.Fatal(err)
	}
	if metric.GetCounter().GetValue() < 1 {
		t.Fatal("foreign container was not rejected")
	}
	t.Logf(
		"channel=%s sandbox=%s pid=%d: real event/profile received; foreign container rejected",
		os.Getenv("HUATUO_KATA_BRIDGE_CHANNEL"),
		selected.SandboxID,
		selected.InitPid,
	)
}
