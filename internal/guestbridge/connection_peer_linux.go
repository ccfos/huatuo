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
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/utils/fileutil"
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
	if peer.HypervisorPID <= 0 || int(cred.Pid) != peer.HypervisorPID {
		return PeerIdentity{}, fmt.Errorf("%w: Unix peer is not the sandbox hypervisor", ErrUntrustedPeer)
	}
	return peer, nil
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
	directory, address, err := socketAddress(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	conn, err := net.DialTimeout("unix", address, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("guestbridge: socket already active: %s", path)
	}
	// Only connection refusal proves an unused socket; permissions and timeouts
	// must not cause deletion of another process's listener.
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	return fileutil.RemoveIfSame(path, info)
}

// Linux limits sockaddr_un to 108 bytes. Open the actual parent directory and
// bind via its short fd path so full sandbox IDs work with longer runtime roots.
func socketAddress(path string) (*os.File, string, error) {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	address := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(directory.Fd()), 10), filepath.Base(path))
	return directory, address, nil
}

type socketListener struct {
	*net.UnixListener
	path     string
	fileInfo os.FileInfo
}

// Close only unlinks the socket this listener bound. A replacement bound by
// another process at the same path must survive our shutdown.
func (l *socketListener) Close() error {
	err := l.UnixListener.Close()
	return errors.Join(err, fileutil.RemoveIfSame(l.path, l.fileInfo))
}

func listenFirecracker(path string) (net.Listener, error) {
	directory, address, err := socketAddress(path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// The fd path expires after bind. Cleanup must use the canonical socket path.
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("guestbridge: inspect bound socket %s: %w", path, err)
	}
	wrapped := &socketListener{UnixListener: listener, path: path, fileInfo: info}
	// Access is restricted by the runtime directory and verified Unix peer PID.
	//nolint:gosec
	if err := os.Chmod(path, 0o666); err != nil {
		_ = wrapped.Close()
		return nil, err
	}
	return wrapped, nil
}
