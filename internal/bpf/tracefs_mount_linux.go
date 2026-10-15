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

package bpf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/procfs"
)

// PrepareTraceFS runs before cilium/ebpf caches tracefs availability. Guest
// kernels may expose only the debugfs layout or omit tracing altogether.
func PrepareTraceFS() error {
	return prepareTraceFS(procfs.DefaultPathByType("sys"))
}

func prepareTraceFS(sysRoot string) error {
	debug := filepath.Join(sysRoot, "kernel/debug")
	debugErr := prepareTracingMount(debug, "debugfs", unix.DEBUGFS_MAGIC)
	trace := filepath.Join(sysRoot, "kernel/tracing")
	if _, err := os.Stat(trace); errors.Is(err, os.ErrNotExist) {
		fallback := filepath.Join(debug, "tracing")
		if _, err := os.Stat(fallback); err == nil {
			trace = fallback
		}
	}
	traceErr := prepareTracingMount(trace, "tracefs", unix.TRACEFS_MAGIC)
	return errors.Join(debugErr, traceErr)
}

func prepareTracingMount(path, kind string, magic int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err == nil && stat.Type == magic {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("prepare %s directory: %w", kind, err)
	}
	if err := unix.Mount(kind, path, kind, unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		if statErr := unix.Statfs(path, &stat); statErr == nil && stat.Type == magic {
			return nil
		}
		return fmt.Errorf("mount %s at %s: %w", kind, path, err)
	}
	return nil
}
