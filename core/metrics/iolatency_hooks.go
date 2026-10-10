// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file selects known raw block tracepoint layouts with one boundary
// probe per changed event, including independently backported interfaces.
package collector

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"

	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_probe.c -o $BPF_DIR/iolatency_probe.o

const (
	ioLatencyQueueBioArgument     = "block_bio_queue_bio_arg"
	ioLatencyRemapBioArgument     = "block_bio_remap_bio_arg"
	ioLatencyRemapRequestArgument = "block_rq_remap_request_arg"
	ioLatencySplitBioArgument     = "block_split_bio_arg"
)

type ioLatencyTracepointDefinition struct {
	program          string
	symbol           string
	constant         string
	boundaryArgument uint32
}

// Queue-less layouts pass bio/rq at slot 0; queue-prefixed layouts use slot 1.
// Remap also carries the source device and sector, giving it 3 or 4 arguments.
var ioLatencyTracepoints = []ioLatencyTracepointDefinition{
	{
		program: "trace_request_complete", symbol: "block_rq_complete",
	},
	{
		program: "trace_bio_queue", symbol: "block_bio_queue",
		constant: ioLatencyQueueBioArgument, boundaryArgument: 1,
	},
	{
		program: "trace_bio_remap", symbol: "block_bio_remap",
		constant: ioLatencyRemapBioArgument, boundaryArgument: 3,
	},
	{
		program: "trace_request_remap", symbol: "block_rq_remap",
		constant: ioLatencyRemapRequestArgument, boundaryArgument: 3,
	},
	{
		program: "trace_bio_split", symbol: "block_split",
		constant: ioLatencySplitBioArgument, boundaryArgument: 2,
	},
}

func loadIOLatencyTracepointArguments() (map[string]any, error) {
	object, err := bpf.LoadBPF("iolatency_probe.o", nil)
	if err != nil {
		return nil, fmt.Errorf("load iolatency argument probe: %w", err)
	}
	constants, probeErr := ioLatencyTracepointArguments(
		func(symbol string, argument uint32) (bool, error) {
			return probeIOLatencyRawArgument(object, symbol, argument)
		},
	)
	if err := object.Close(); err != nil {
		return nil, errors.Join(probeErr, fmt.Errorf("close iolatency argument probe: %w", err))
	}
	return constants, probeErr
}

func probeIOLatencyRawArgument(
	object bpf.BPF, symbol string, argument uint32,
) (bool, error) {
	program := fmt.Sprintf("probe_arg%d", argument)
	if err := object.AttachWithOptions([]bpf.AttachOption{{
		ProgramName: program,
		Symbol:      symbol,
	}}); err != nil {
		// The programs have fixed context reads and no pointer accesses.
		// Only attach-time EINVAL marks an argument boundary; load,
		// permission, transport, and cleanup errors remain ordinary errors.
		if errors.Is(err, unix.EINVAL) {
			return false, nil
		}
		return false, err
	}
	if err := bpf.DetachProgram(object, program); err != nil {
		return false, fmt.Errorf("detach argument probe: %w", err)
	}
	return true, nil
}

func ioLatencyTracepointArguments(
	probe func(string, uint32) (bool, error),
) (map[string]any, error) {
	constants := make(map[string]any, len(ioLatencyTracepoints))

	for _, definition := range ioLatencyTracepoints {
		if definition.constant == "" {
			continue
		}
		// This slot exists only in the queue-prefixed layout. Qualification
		// tests check full argument counts on each supported kernel.
		available, err := probe(definition.symbol, definition.boundaryArgument)
		if err != nil {
			return nil, fmt.Errorf("probe iolatency %s argument %d: %w",
				definition.symbol, definition.boundaryArgument, err)
		}
		constants[definition.constant] = uint32(0)
		if available {
			constants[definition.constant] = uint32(1)
		}
	}
	return constants, nil
}
