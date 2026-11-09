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

package transport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/ccfos/huatuo/internal/utils/fileutil"
)

type udsListener struct {
	net.Listener
	path     string
	fileInfo os.FileInfo
}

func (l *udsListener) Close() error {
	closeErr := l.Listener.Close()
	removeErr := fileutil.RemoveIfSame(l.path, l.fileInfo)
	return errors.Join(closeErr, removeErr)
}

// ListenUDS binds a Unix socket at path. The parent directory must not be
// writable by untrusted users because permission changes and removal are
// pathname-based operations.
func ListenUDS(path string) (net.Listener, error) {
	if err := prepareSocketPath(path); err != nil {
		return nil, err
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %s: %w", path, err)
	}
	if unixListener, ok := l.(*net.UnixListener); ok {
		// Own unlinking below so Close cannot delete a path that has been
		// replaced since this listener was bound.
		unixListener.SetUnlinkOnClose(false)
	}
	// Capture the bound inode first so cleanup can never delete a path that
	// another process replaced before chmod or close.
	info, err := os.Lstat(path)
	if err != nil {
		// The path may no longer belong to this listener; leave it alone.
		_ = l.Close()
		return nil, fmt.Errorf("transport: inspect bound socket %s: %w", path, err)
	}
	// chmod is the security boundary for who can connect; if it fails the socket
	// would silently keep the umask-derived permissions, so refuse to expose it.
	if err := os.Chmod(path, 0o660); err != nil {
		_ = l.Close()
		_ = fileutil.RemoveIfSame(path, info)
		return nil, fmt.Errorf("transport: chmod %s: %w", path, err)
	}

	return &udsListener{Listener: l, path: path, fileInfo: info}, nil
}

func prepareSocketPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("transport: inspect socket path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("transport: socket path exists and is not a Unix socket: %s", path)
	}

	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("transport: socket already has an active listener: %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("transport: probe existing socket %s: %w", path, err)
	}

	if err := fileutil.RemoveIfSame(path, info); err != nil {
		return err
	}
	return nil
}

// DialUDS connects to a Unix socket at path.
func DialUDS(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}
