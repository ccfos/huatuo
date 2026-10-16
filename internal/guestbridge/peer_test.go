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
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/pod"

	"github.com/mdlayher/vsock"
)

// TestQemuGuestCID verifies Host QEMU argument parsing: PCI, MMIO, and guest= name forms are supported;
// mismatched Sandboxes, non-QEMU processes, reserved CIDs, overflow, and duplicate CIDs are rejected.
func TestQemuGuestCID(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		valid bool
	}{
		{"pci", []string{"/opt/kata/bin/qemu-system-x86_64", "-name", "sandbox-workload", "-device", "vhost-vsock-pci,guest-cid=4105"}, true},
		{"mmio", []string{"qemu-system-aarch64", "-name", "guest=sandbox-workload,debug-threads=on", "-device", "vhost-vsock-device,guest-cid=4105"}, true},
		{"different sandbox", []string{"qemu-system-x86_64", "-name", "sandbox-other", "-device", "vhost-vsock-pci,guest-cid=4105"}, false},
		{"different executable", []string{"workload", "-name", "sandbox-workload", "-device", "vhost-vsock-pci,guest-cid=4105"}, false},
		{"host CID", []string{"qemu-system-x86_64", "-name", "sandbox-workload", "-device", "vhost-vsock-pci,guest-cid=2"}, false},
		{"overflow CID", []string{"qemu-system-x86_64", "-name", "sandbox-workload", "-device", "vhost-vsock-pci,guest-cid=4294967296"}, false},
		{"duplicate CID", []string{"qemu-system-x86_64", "-name", "sandbox-workload", "-device", "vhost-vsock-pci,guest-cid=4105,guest-cid=4106"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cid, err := qemuGuestCID([]byte(strings.Join(tc.args, "\x00")+"\x00"), "workload")
			if tc.valid {
				if err != nil || cid != 4105 {
					t.Errorf("CID=%d err=%v", cid, err)
				}
			} else if err == nil {
				t.Errorf("accepted invalid identity: CID=%d", cid)
			}
		})
	}
}

// TestPeerResolution verifies that connection identity comes from Host metadata: a CID matches only its Sandbox;
// Firecracker uses the listener directory identity, and unknown CIDs or CIDs shared by multiple Sandboxes are rejected.
func TestPeerResolution(t *testing.T) {
	containers := map[string]*pod.Container{
		"orders":   {ID: "orders", InitPid: 4105, SandboxID: "orders-vm", RuntimeType: "io.containerd.kata-qemu.v2", Labels: map[string]any{"HostNamespace": "application"}},
		"payments": {ID: "payments", InitPid: 4106, SandboxID: "payments-vm", RuntimeType: "io.containerd.kata-qemu.v2", Labels: map[string]any{"HostNamespace": "application"}},
	}
	readFile := func(path string) ([]byte, error) {
		sandbox, cid := "orders-vm", "5105"
		if path == "/proc/4106/cmdline" {
			sandbox, cid = "payments-vm", "5106"
		}
		return []byte(strings.Join([]string{"qemu-system-x86_64", "-name", "sandbox-" + sandbox, "-device", "vhost-vsock-pci,guest-cid=" + cid}, "\x00")), nil
	}
	peer, err := resolvePeerFromContainers(&vsock.Addr{ContextID: 5105}, "", containers, readFile)
	if err != nil || peer.SandboxID != "orders-vm" || peer.Namespace != "application" {
		t.Errorf("peer=%+v err=%v", peer, err)
	}
	peer, err = resolvePeerFromContainers(&net.UnixAddr{Name: "kata.hvsock", Net: "unix"}, "payments-vm", containers, readFile)
	if err != nil || peer.SandboxID != "payments-vm" {
		t.Errorf("Firecracker peer=%+v err=%v", peer, err)
	}
	if _, err := resolvePeerFromContainers(&vsock.Addr{ContextID: 5999}, "", containers, readFile); !errors.Is(err, errUnknownPeerSandbox) {
		t.Errorf("unknown CID error=%v", err)
	}
	duplicate := func(path string) ([]byte, error) {
		raw, err := readFile(path)
		return []byte(strings.ReplaceAll(string(raw), "5106", "5105")), err
	}
	if _, err := resolvePeerFromContainers(&vsock.Addr{ContextID: 5105}, "", containers, duplicate); err == nil {
		t.Errorf("ambiguous CID accepted")
	}
}

// TestWaitPeerStops verifies timeout, cancellation, and immediate rejection of
// non-retryable identity errors without waiting for another discovery attempt.
func TestWaitPeerStops(t *testing.T) {
	invalid := errors.New("invalid peer")
	for _, tc := range []struct {
		name       string
		resolveErr error
		cancel     bool
		wantErr    error
	}{
		{"unknown times out", errUnknownPeerSandbox, false, context.DeadlineExceeded},
		{"shutdown cancels wait", errUnknownPeerSandbox, true, context.Canceled},
		{"invalid is not retried", invalid, false, invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			bridge := &Bridge{
				handshakeTimeout: 20 * time.Millisecond,
				resolvePeer: func(net.Addr, string) (PeerIdentity, error) {
					calls++
					if tc.cancel {
						cancel()
					}
					return PeerIdentity{}, tc.resolveErr
				},
			}
			peer, err := bridge.waitPeer(ctx, &vsock.Addr{ContextID: 5105}, "")
			if !errors.Is(err, tc.wantErr) || peer != (PeerIdentity{}) || calls != 1 {
				t.Errorf("peer=%+v err=%v calls=%d; want err=%v and one lookup", peer, err, calls, tc.wantErr)
			}
		})
	}
}
