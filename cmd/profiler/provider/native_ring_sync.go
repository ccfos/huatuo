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

package provider

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
)

// Linux UAPI include/uapi/linux/membarrier.h; x/sys/unix omits these constants.
const (
	membarrierCmdQuery  = 0
	membarrierCmdGlobal = 1
)

// A nil synchronizer selects the legacy blocking read strategy for this instance.
// It is a compatibility fallback, not proof that previous writers have exited.
func detectNativeWriterSynchronization() func() error {
	supported, _, errno := unix.Syscall(unix.SYS_MEMBARRIER, membarrierCmdQuery, 0, 0)
	if errno == 0 && supported&membarrierCmdGlobal != 0 {
		return synchronizeNativeWriters
	}

	var reason error = errors.New("kernel does not support MEMBARRIER_CMD_GLOBAL")
	if errno != 0 {
		reason = errno
	}

	log.WithField("operation", "MEMBARRIER_CMD_GLOBAL").WithField("reason", reason).
		WithField("read_timeout", "500ms").Warn("native profiler writer synchronization unavailable; falling back to blocking reads; samples may be lost and physical_usage collection may fail")

	return nil
}

// synchronizeNativeWriters relies on Linux GLOBAL membarrier using a sched-RCU
// grace period (4.18) or unified RCU (5.0+). Non-sleepable tracing BPF runs with
// preemption disabled, so all writers that selected the old parity have exited
// before this returns. EXPEDITED commands only issue IPIs and are not substitutes.
// An RCU stall can delay this syscall; an arbitrary timeout cannot prove quiescence.
func synchronizeNativeWriters() error {
	_, _, errno := unix.Syscall(unix.SYS_MEMBARRIER, membarrierCmdGlobal, 0, 0)
	if errno != 0 {
		return errno
	}

	return nil
}
