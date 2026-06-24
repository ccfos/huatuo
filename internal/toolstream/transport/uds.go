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
	"fmt"
	"net"
	"os"
)

type udsListener struct {
	net.Listener
	path string
}

func (l *udsListener) Close() error {
	_ = os.Remove(l.path)
	return l.Listener.Close()
}

// ListenUDS binds a Unix socket at path; returns an error if path already exists.
func ListenUDS(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("transport: socket path already exists: %s", path)
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

// DialUDS connects to a Unix socket at path.
func DialUDS(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}
