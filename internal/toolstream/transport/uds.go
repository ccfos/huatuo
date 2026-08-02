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
)

type udsListener struct {
	net.Listener
	path string
}

func (l *udsListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}

// ListenUDS binds a Unix socket at path.
func ListenUDS(path string) (net.Listener, error) {
	if err := prepareSocketPath(path); err != nil {
		return nil, err
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %s: %w", path, err)
	}

	// chmod is the security boundary for who can connect; if it fails the socket
	// would silently keep the umask-derived permissions, so refuse to expose it.
	if err := os.Chmod(path, 0o660); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("transport: chmod %s: %w", path, err)
	}

	return &udsListener{Listener: l, path: path}, nil
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
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("transport: probe existing socket %s: %w", path, err)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("transport: remove stale socket %s: %w", path, err)
	}
	return nil
}

// DialUDS connects to a Unix socket at path.
func DialUDS(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}
