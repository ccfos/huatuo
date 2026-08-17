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

package netutil

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestNetNamespaceInumByPID(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name:    "valid current pid",
			pid:     os.Getpid(),
			wantErr: false,
		},
		{
			name:    "invalid pid 0",
			pid:     0,
			wantErr: true,
		},
		{
			name:    "invalid negative pid",
			pid:     -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NetNamespaceInumByPID(tt.pid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NetNamespaceInumByPID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("NetNamespaceInumByPID() got = %v, want non-zero inum", got)
			}
		})
	}
}

type fakeNamespaceHandle struct {
	fd       uintptr
	closed   bool
	closeErr error
}

func (h *fakeNamespaceHandle) Fd() uintptr {
	return h.fd
}

func (h *fakeNamespaceHandle) Close() error {
	h.closed = true
	return h.closeErr
}

func TestNetNamespaceCookieRestoresCallingThread(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	target := &fakeNamespaceHandle{fd: 20}
	var entered []int
	socketClosed := false
	lockCalls := 0
	unlockCalls := 0

	cookie, err := netNamespaceCookieWithThreadOperations(
		targetPath,
		netNamespaceCookieOperations{
			open: namespaceOpenFixture(t, targetPath, current, target),
			setns: func(fd, namespaceType int) error {
				assert.Equal(t, unix.CLONE_NEWNET, namespaceType)
				entered = append(entered, fd)
				return nil
			},
			socket: func(domain, typ, protocol int) (int, error) {
				assert.Equal(t, unix.AF_INET, domain)
				assert.Equal(t, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, typ)
				assert.Zero(t, protocol)
				return 30, nil
			},
			getsockopt: func(fd, level, option int) (uint64, error) {
				assert.Equal(t, 30, fd)
				assert.Equal(t, unix.SOL_SOCKET, level)
				assert.Equal(t, unix.SO_NETNS_COOKIE, option)
				return 2026, nil
			},
			close: func(fd int) error {
				assert.Equal(t, 30, fd)
				socketClosed = true
				return nil
			},
		},
		netNamespaceThreadOperations{
			lock:   func() { lockCalls++ },
			unlock: func() { unlockCalls++ },
		},
	)

	require.NoError(t, err)
	assert.Equal(t, uint64(2026), cookie)
	assert.Equal(t, []int{int(target.fd), int(current.fd)}, entered)
	assert.True(t, socketClosed)
	assert.True(t, target.closed)
	assert.True(t, current.closed)
	assert.Equal(t, 1, lockCalls)
	assert.Equal(t, 1, unlockCalls)
}

func TestNetNamespaceCookiePreservesOldKernelCompatibility(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	target := &fakeNamespaceHandle{fd: 20}
	socketClosed := false

	cookie, err := netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open:  namespaceOpenFixture(t, targetPath, current, target),
		setns: func(int, int) error { return nil },
		socket: func(int, int, int) (int, error) {
			return 30, nil
		},
		getsockopt: func(int, int, int) (uint64, error) {
			return 0, unix.ENOPROTOOPT
		},
		close: func(int) error {
			socketClosed = true
			return nil
		},
	})

	require.NoError(t, err)
	assert.Zero(t, cookie)
	assert.True(t, socketClosed)
	assert.True(t, target.closed)
	assert.True(t, current.closed)
}

func TestNetNamespaceCookieRestoresAfterSocketFailure(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	target := &fakeNamespaceHandle{fd: 20}
	socketErr := errors.New("socket failed")
	var entered []int

	cookie, err := netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open: namespaceOpenFixture(t, targetPath, current, target),
		setns: func(fd, _ int) error {
			entered = append(entered, fd)
			return nil
		},
		socket: func(int, int, int) (int, error) {
			return -1, socketErr
		},
		getsockopt: func(int, int, int) (uint64, error) {
			return 0, errors.New("getsockopt must not be called")
		},
		close: func(int) error {
			return errors.New("close must not be called")
		},
	})

	assert.Zero(t, cookie)
	require.ErrorIs(t, err, socketErr)
	assert.Equal(t, []int{int(target.fd), int(current.fd)}, entered)
	assert.True(t, target.closed)
	assert.True(t, current.closed)
}

func TestNetNamespaceCookieReportsRestoreAndSocketCloseFailures(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	target := &fakeNamespaceHandle{fd: 20}
	restoreErr := errors.New("restore failed")
	closeErr := errors.New("socket close failed")
	lockCalls := 0
	unlockCalls := 0

	cookie, err := netNamespaceCookieWithThreadOperations(
		targetPath,
		netNamespaceCookieOperations{
			open: namespaceOpenFixture(t, targetPath, current, target),
			setns: func(fd, _ int) error {
				if fd == int(current.fd) {
					return restoreErr
				}
				return nil
			},
			socket: func(int, int, int) (int, error) { return 30, nil },
			getsockopt: func(int, int, int) (uint64, error) {
				return 2026, nil
			},
			close: func(int) error { return closeErr },
		},
		netNamespaceThreadOperations{
			lock:   func() { lockCalls++ },
			unlock: func() { unlockCalls++ },
		},
	)

	assert.Equal(t, uint64(2026), cookie)
	require.ErrorIs(t, err, restoreErr)
	require.ErrorIs(t, err, closeErr)
	assert.Contains(t, err.Error(), "restore current thread network namespace")
	assert.Contains(t, err.Error(), "close network namespace cookie socket")
	assert.True(t, target.closed)
	assert.True(t, current.closed)
	assert.Equal(t, 1, lockCalls)
	assert.Zero(t, unlockCalls)
}

func TestNetNamespaceCookieCleansUpTargetEntryFailure(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	target := &fakeNamespaceHandle{fd: 20}
	enterErr := errors.New("target setns failed")
	socketCalled := false

	cookie, err := netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open: namespaceOpenFixture(t, targetPath, current, target),
		setns: func(int, int) error {
			return enterErr
		},
		socket: func(int, int, int) (int, error) {
			socketCalled = true
			return 30, nil
		},
		getsockopt: func(int, int, int) (uint64, error) { return 0, nil },
		close:      func(int) error { return nil },
	})

	assert.Zero(t, cookie)
	require.ErrorIs(t, err, enterErr)
	assert.False(t, socketCalled)
	assert.True(t, target.closed)
	assert.True(t, current.closed)
}

func TestNetNamespaceCookieCleansUpTargetOpenFailure(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	current := &fakeNamespaceHandle{fd: 10}
	openErr := errors.New("target open failed")

	cookie, err := netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open: func(path string) (namespaceHandle, error) {
			switch path {
			case currentThreadNetNamespace:
				return current, nil
			case targetPath:
				return nil, openErr
			default:
				return nil, errors.New("unexpected namespace path: " + path)
			}
		},
		setns: func(int, int) error {
			return errors.New("setns must not be called")
		},
		socket: func(int, int, int) (int, error) {
			return -1, errors.New("socket must not be called")
		},
		getsockopt: func(int, int, int) (uint64, error) {
			return 0, errors.New("getsockopt must not be called")
		},
		close: func(int) error {
			return errors.New("socket close must not be called")
		},
	})

	assert.Zero(t, cookie)
	require.ErrorIs(t, err, openErr)
	assert.True(t, current.closed)
}

func TestNetNamespaceCookiePreservesOperationAndNamespaceCloseFailures(t *testing.T) {
	const targetPath = "/proc/4242/ns/net"
	currentCloseErr := errors.New("current close failed")
	targetCloseErr := errors.New("target close failed")
	getCookieErr := errors.New("getsockopt failed")
	current := &fakeNamespaceHandle{fd: 10, closeErr: currentCloseErr}
	target := &fakeNamespaceHandle{fd: 20, closeErr: targetCloseErr}

	cookie, err := netNamespaceCookie(targetPath, netNamespaceCookieOperations{
		open:  namespaceOpenFixture(t, targetPath, current, target),
		setns: func(int, int) error { return nil },
		socket: func(int, int, int) (int, error) {
			return 30, nil
		},
		getsockopt: func(int, int, int) (uint64, error) {
			return 0, getCookieErr
		},
		close: func(int) error { return nil },
	})

	assert.Zero(t, cookie)
	require.ErrorIs(t, err, getCookieErr)
	require.ErrorIs(t, err, targetCloseErr)
	require.ErrorIs(t, err, currentCloseErr)
	assert.Contains(t, err.Error(), "read SO_NETNS_COOKIE")
	assert.Contains(t, err.Error(), "close target network namespace")
	assert.Contains(t, err.Error(), "close current thread network namespace")
	assert.True(t, target.closed)
	assert.True(t, current.closed)
}

func namespaceOpenFixture(
	t *testing.T,
	targetPath string,
	current, target namespaceHandle,
) func(string) (namespaceHandle, error) {
	t.Helper()
	return func(path string) (namespaceHandle, error) {
		switch path {
		case currentThreadNetNamespace:
			return current, nil
		case targetPath:
			return target, nil
		default:
			return nil, errors.New("unexpected namespace path: " + path)
		}
	}
}
