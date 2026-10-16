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
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/pod"
)

func (b *Bridge) waitConnectionPeer(ctx context.Context, conn net.Conn, sandboxID string) (PeerIdentity, error) {
	if sandboxID == "" {
		return b.waitPeer(ctx, conn.RemoteAddr(), "")
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return PeerIdentity{}, fmt.Errorf("guestbridge: Unix peer credentials unavailable")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return PeerIdentity{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return PeerIdentity{}, err
	}
	if credErr != nil {
		return PeerIdentity{}, credErr
	}
	peer, err := b.waitPeer(ctx, conn.RemoteAddr(), sandboxID)
	if err != nil {
		return PeerIdentity{}, err
	}
	containers, err := pod.Containers()
	if err != nil {
		return PeerIdentity{}, err
	}
	return validateFirecrackerPeer(int(cred.Pid), containers, peer)
}

func validateFirecrackerPeer(pid int, containers map[string]*pod.Container, peer PeerIdentity) (PeerIdentity, error) {
	for _, c := range containers {
		if c.HostKataSandboxID() == peer.SandboxID && c.InitPid == pid && c.LabelHostNamespace() == peer.Namespace {
			return peer, nil
		}
	}
	return PeerIdentity{}, fmt.Errorf("%w: Unix peer is not the sandbox hypervisor", ErrUntrustedPeer)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("guestbridge: refusing to remove non-socket %s", path)
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("guestbridge: socket already active: %s", path)
	}
	// Only connection refusal proves an unused socket; permissions and timeouts
	// must not cause deletion of another process's listener.
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	return os.Remove(path)
}
