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
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"

	"golang.org/x/sys/unix"
)

const (
	ioHealthEventBlockError = 1
)

// ioHealthPerfEvent mirrors struct health_event in bpf/io_health.c.
type ioHealthPerfEvent struct {
	Sector    uint64
	Dev       uint32
	Status    int32
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

func attachIOHealthHooks(
	object bpf.BPF,
) (attached int) {
	blockHook := ioHealthBlockErrorHook
	err := bpf.AttachIndependently(object, &bpf.AttachOption{
		ProgramName: blockHook.program,
		Symbol:      blockHook.symbol,
	})
	if errors.Is(err, unix.ENOENT) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, types.ErrNotSupported) {
		blockHook = ioHealthBlockCompleteHook
		err = bpf.AttachIndependently(object, &bpf.AttachOption{
			ProgramName: blockHook.program,
			Symbol:      blockHook.symbol,
		})
	}
	if err == nil {
		attached++
	} else {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			blockHook.symbol,
			err,
		)
	}

	return attached
}
