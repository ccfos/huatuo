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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/pod"

	"github.com/mdlayher/vsock"
)

// PeerIdentity is resolved by the Host, never from the Guest handshake or JSON.
// It is bound once per connection so a reused CID cannot reassign a live session.
type PeerIdentity struct {
	SandboxID     string
	HypervisorPID int
	Namespace     string
}

type peerIdentityKey struct{}

var errUnknownPeerSandbox = errors.New("guestbridge: unknown peer sandbox")

// waitPeer allows Host discovery to catch up before authenticating a new Guest.
// Only missing metadata is retryable; invalid or ambiguous peers fail immediately.
func (bridge *Bridge) waitPeer(ctx context.Context, addr net.Addr, sandboxID string) (PeerIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, bridge.handshakeTimeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return PeerIdentity{}, err
		}
		peer, err := bridge.resolvePeer(addr, sandboxID)
		if ctx.Err() != nil {
			return PeerIdentity{}, ctx.Err()
		}
		if !errors.Is(err, errUnknownPeerSandbox) {
			return peer, err
		}
		select {
		case <-ctx.Done():
			return PeerIdentity{}, fmt.Errorf("%w: %w", errUnknownPeerSandbox, ctx.Err())
		case <-ticker.C:
		}
	}
}

func resolvePeer(addr net.Addr, sandboxID string) (PeerIdentity, error) {
	containers, err := pod.Containers()
	if err != nil {
		return PeerIdentity{}, err
	}
	return resolvePeerFromContainers(addr, sandboxID, containers, os.ReadFile)
}

func resolvePeerFromContainers(
	addr net.Addr,
	sandboxID string,
	containers map[string]*pod.Container,
	readFile func(string) ([]byte, error),
) (PeerIdentity, error) {
	var cid uint32
	if sandboxID == "" {
		peer, ok := addr.(*vsock.Addr)
		if !ok || peer == nil || peer.ContextID <= vsock.Host || peer.ContextID == ^uint32(0) {
			return PeerIdentity{}, fmt.Errorf("guestbridge: invalid VSOCK peer")
		}
		cid = peer.ContextID
	}
	identity := PeerIdentity{}
	type processKey struct {
		PID     int
		Sandbox string
	}
	checked := make(map[processKey]uint32)
	for _, container := range containers {
		candidate := container.HostKataSandboxID()
		if candidate == "" {
			continue
		}
		if sandboxID != "" {
			if candidate != sandboxID {
				continue
			}
		} else {
			key := processKey{PID: container.InitPid, Sandbox: candidate}
			guestCID, found := checked[key]
			if !found {
				raw, err := readFile(filepath.Join("/proc", strconv.Itoa(container.InitPid), "cmdline"))
				if err != nil {
					continue
				}
				guestCID, err = qemuGuestCID(raw, candidate)
				if err != nil {
					continue
				}
				checked[key] = guestCID
			}
			if guestCID != cid {
				continue
			}
		}
		namespace, ok := container.Labels["HostNamespace"].(string)
		if !ok || namespace == "" {
			continue
		}
		if identity.SandboxID != "" &&
			(identity.SandboxID != candidate ||
				identity.Namespace != namespace ||
				identity.HypervisorPID != container.InitPid) {
			return PeerIdentity{}, fmt.Errorf("guestbridge: ambiguous peer identity")
		}
		identity = PeerIdentity{SandboxID: candidate, Namespace: namespace, HypervisorPID: container.InitPid}
	}
	if identity.SandboxID == "" {
		return PeerIdentity{}, errUnknownPeerSandbox
	}
	return identity, nil
}

// qemuGuestCID checks the runtime-provided PID's VM name as well as its VSOCK
// device, avoiding attribution to a different process after PID reuse.
func qemuGuestCID(raw []byte, sandboxID string) (uint32, error) {
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(args) == 0 || !strings.HasPrefix(filepath.Base(args[0]), "qemu-system-") {
		return 0, fmt.Errorf("guestbridge: not a QEMU process")
	}
	var cid uint64
	matchedName := false
	for i := 1; i+1 < len(args); i++ {
		switch args[i] {
		case "-name":
			name := strings.TrimPrefix(strings.Split(args[i+1], ",")[0], "guest=")
			matchedName = name == "sandbox-"+sandboxID
		case "-device":
			parts := strings.Split(args[i+1], ",")
			device := strings.TrimPrefix(parts[0], "driver=")
			if device != "vhost-vsock-pci" && device != "vhost-vsock-device" && device != "vhost-vsock-ccw" {
				continue
			}
			for _, part := range parts[1:] {
				if !strings.HasPrefix(part, "guest-cid=") {
					continue
				}
				if cid != 0 {
					return 0, fmt.Errorf("guestbridge: multiple guest CIDs")
				}
				value, err := strconv.ParseUint(strings.TrimPrefix(part, "guest-cid="), 10, 32)
				if err != nil || value <= uint64(vsock.Host) || value == uint64(^uint32(0)) {
					return 0, fmt.Errorf("guestbridge: invalid guest CID")
				}
				cid = value
			}
		}
	}
	if !matchedName || cid == 0 {
		return 0, fmt.Errorf("guestbridge: QEMU sandbox or CID mismatch")
	}
	return uint32(cid), nil
}
