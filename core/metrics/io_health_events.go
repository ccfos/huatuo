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

package collector

import (
	"errors"
	"strconv"
	"strings"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"

	"golang.org/x/sys/unix"
)

const (
	ioHealthEventBlockError = iota + 1
	ioHealthEventSCSITimeout
	ioHealthEventSCSIDispatchError
)

// ioHealthPerfEvent mirrors struct health_event in bpf/io_health.c.
type ioHealthPerfEvent struct {
	Sector    uint64
	Dev       uint32
	Status    int32
	Host      uint32
	Channel   uint32
	Target    uint32
	LUN       uint32
	Type      uint8
	Operation uint8
	Pad       [6]uint8
}

type ioHealthHook struct {
	program string
	symbol  string
}

var ioHealthBlockErrorHook = ioHealthHook{
	program: "trace_block_rq_error",
	symbol:  "block/block_rq_error",
}

var ioHealthBlockCompleteHook = ioHealthHook{
	program: "trace_block_rq_complete_error",
	symbol:  "block_rq_complete",
}

var ioHealthHooks = []ioHealthHook{
	{program: "trace_scsi_timeout", symbol: "scsi/scsi_dispatch_cmd_timeout"},
	{program: "trace_scsi_dispatch_error", symbol: "scsi/scsi_dispatch_cmd_error"},
}

func attachIOHealthHooks(
	object bpf.BPF,
	legacyBlockCompleteSupported bool,
) (attached int) {
	err := bpf.AttachIndependently(object, bpf.AttachOption{
		ProgramName: ioHealthBlockErrorHook.program,
		Symbol:      ioHealthBlockErrorHook.symbol,
	})
	if err == nil {
		attached++
	} else {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			ioHealthBlockErrorHook.symbol,
			err,
		)
		if errors.Is(err, unix.ENOENT) && legacyBlockCompleteSupported {
			if err := bpf.AttachIndependently(object, bpf.AttachOption{
				ProgramName: ioHealthBlockCompleteHook.program,
				Symbol:      ioHealthBlockCompleteHook.symbol,
			}); err != nil {
				log.Warnf(
					"io_health: attach optional hook %s: %v",
					ioHealthBlockCompleteHook.symbol,
					err,
				)
			} else {
				attached++
			}
		}
	}

	for _, hook := range ioHealthHooks {
		if err := bpf.AttachIndependently(object, bpf.AttachOption{
			ProgramName: hook.program,
			Symbol:      hook.symbol,
		}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			continue
		}
		attached++
	}

	return attached
}

func detectIOHealthKernelRelease() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return ""
	}

	release := make([]byte, 0, len(name.Release))
	for _, value := range name.Release {
		if value == 0 {
			break
		}
		release = append(release, byte(value))
	}
	return string(release)
}

func ioHealthKernelVersion(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func ioHealthLegacyBlockCompleteSupported(release string) bool {
	major, minor, ok := ioHealthKernelVersion(release)
	if !ok {
		return false
	}
	return major < 5 || (major == 5 && minor <= 15)
}
