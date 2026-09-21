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

// Initialized software blk-crypto fallback excludes this feature for the
// session, even if no longer in use; activation after startup is not watched.
// Known raw tracepoint layouts are selected with one boundary probe per event.
// Temporary data probes are unloaded before IO tracing.
package collector

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_probe.c -o $BPF_DIR/iolatency_probe.o

const (
	ioLatencyQueueBioArgument     = "block_bio_queue_bio_arg"
	ioLatencyRemapBioArgument     = "block_bio_remap_bio_arg"
	ioLatencyRemapRequestArgument = "block_rq_remap_request_arg"
	ioLatencyBioTraceFlag         = "io_latency_bio_trace_completion"
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
		program: "trace_request_complete",
		symbol:  "block_rq_complete",
	},
	{
		program:          "trace_bio_queue",
		symbol:           "block_bio_queue",
		constant:         ioLatencyQueueBioArgument,
		boundaryArgument: 1,
	},
	{
		program:          "trace_bio_remap",
		symbol:           "block_bio_remap",
		constant:         ioLatencyRemapBioArgument,
		boundaryArgument: 3,
	},
	{
		program:          "trace_request_remap",
		symbol:           "block_rq_remap",
		constant:         ioLatencyRemapRequestArgument,
		boundaryArgument: 3,
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
	if err := object.Detach(); err != nil {
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

const (
	ioLatencyRootBlkcg = "io_latency_root_blkcg"

	// The startup probe reads one bool per mode. This capacity accommodates
	// the complete runtime enum; larger arrays fail the check explicitly.
	ioLatencyCryptoModeCapacity = uint64(abi.IOLatencyCryptoModeCapacity)
)

func loadIOLatencyKernelConstants() (map[string]any, error) {
	btfPath := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
	spec, err := btf.LoadSpec(btfPath)
	if err != nil {
		return nil, fmt.Errorf("%w: iolatency: load kernel BTF %s: %w",
			types.ErrTracingStopped, btfPath, err)
	}
	addresses, err := symbol.KsymbolSearchAddr(
		[]string{"tfms_inited", "blkcg_root"}, symbol.KsymbolLookupData,
	)
	if err != nil {
		return nil, fmt.Errorf("read iolatency kernel symbols: %w", err)
	}
	if address := addresses["tfms_inited"].Start; address != 0 {
		if err := checkIOLatencyCryptoFallback(spec, address); err != nil {
			return nil, err
		}
	} else {
		enabled, err := ioLatencyCryptoFallbackConfigured()
		if err != nil {
			return nil, err
		}
		if enabled {
			return nil, fmt.Errorf("iolatency: configured blk-crypto fallback has no visible tfms_inited symbol")
		}
	}
	flag, err := ioLatencyBioTraceCompletion(spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		ioLatencyRootBlkcg:    addresses["blkcg_root"].Start,
		ioLatencyBioTraceFlag: flag,
	}, nil
}

func ioLatencyBioTraceCompletion(spec *btf.Spec) (uint32, error) {
	// The legacy bio flags were macros: BIO_TRACE_COMPLETION was bit 10.
	// Enum-based flags have moved since then; resolve their actual value,
	// including anonymous enums, without selecting by kernel release.
	flag := uint32(10)
	found := false
	types := spec.Iterate()
	for types.Next() {
		enum, ok := types.Type.(*btf.Enum)
		if !ok {
			continue
		}
		for _, value := range enum.Values {
			if value.Name != "BIO_TRACE_COMPLETION" {
				continue
			}
			if value.Value >= 16 || (found && uint64(flag) != value.Value) {
				return 0, fmt.Errorf("iolatency: unsupported BIO_TRACE_COMPLETION value %d", value.Value)
			}
			flag = uint32(value.Value)
			found = true
		}
	}
	return flag, nil
}

func ioLatencyCryptoFallbackConfigured() (bool, error) {
	data, err := os.ReadFile(procfs.Path("config.gz"))
	if err == nil {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return false, fmt.Errorf("read running kernel config: %w", err)
		}
		data, err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return false, fmt.Errorf("decode running kernel config: %w", errors.Join(err, closeErr))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		// The release locates the running kernel's config; it does not select
		// a compatibility path or infer whether blk-crypto is supported.
		release, err := os.ReadFile(procfs.Path("sys", "kernel", "osrelease"))
		if err != nil {
			return false, fmt.Errorf("locate running kernel config: %w", err)
		}
		// Container deployments share the host PID namespace. PID 1's root
		// exposes the host configuration instead of the image's /boot.
		path := procfs.Path("1", "root", "boot",
			"config-"+strings.TrimSpace(string(release)))
		data, err = os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("read running kernel config %s: %w", path, err)
		}
	} else {
		return false, fmt.Errorf("read running kernel config: %w", err)
	}

	const option = "CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK="
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), option)
		if !found {
			continue
		}
		switch value {
		case "y":
			return true, nil
		case "n":
			return false, nil
		default:
			return false, fmt.Errorf("invalid kernel config value %s%s", option, value)
		}
	}
	return false, nil
}

func checkIOLatencyCryptoFallback(spec *btf.Spec, address uint64) error {
	modes, err := ioLatencyCryptoModeCount(spec)
	if err != nil {
		return err
	}
	object, err := bpf.LoadBPF("iolatency_probe.o", map[string]any{
		"io_latency_tfms_inited":  address,
		"io_latency_crypto_modes": modes,
	})
	if err != nil {
		return fmt.Errorf("load blk-crypto data probe: %w", err)
	}
	probeErr := readIOLatencyCryptoProbe(object)
	if err := object.Close(); err != nil {
		return errors.Join(probeErr, fmt.Errorf("close blk-crypto data probe: %w", err))
	}
	return probeErr
}

func ioLatencyCryptoModeCount(spec *btf.Spec) (uint32, error) {
	candidates, err := spec.AnyTypesByName("blk_crypto_mode_num")
	if err != nil {
		return 0, fmt.Errorf("read blk-crypto mode enum: %w", err)
	}
	var count uint32
	for _, candidate := range candidates {
		modes, ok := candidate.(*btf.Enum)
		if !ok {
			continue
		}
		var bound uint64
		for _, mode := range modes.Values {
			if mode.Name == "BLK_ENCRYPTION_MODE_MAX" {
				bound = mode.Value
				break
			}
		}
		if bound == 0 || bound > ioLatencyCryptoModeCapacity {
			return 0, fmt.Errorf("blk-crypto mode count %d is outside startup probe range 1..%d",
				bound, ioLatencyCryptoModeCapacity)
		}
		if count != 0 && uint64(count) != bound {
			return 0, fmt.Errorf("blk-crypto mode enums have conflicting bounds %d and %d", count, bound)
		}
		count = uint32(bound)
	}
	if count == 0 {
		return 0, fmt.Errorf("blk-crypto mode enum has no BLK_ENCRYPTION_MODE_MAX")
	}
	return count, nil
}

func readIOLatencyCryptoProbe(object bpf.BPF) error {
	if err := object.AttachWithOptions([]bpf.AttachOption{{
		ProgramName: "probe_crypto_fallback",
		Symbol:      "sys_enter",
	}}); err != nil {
		return fmt.Errorf("attach blk-crypto data probe: %w", err)
	}
	// The map-read syscall also triggers sys_enter on the BPF host.
	value, err := object.ReadMap(object.MapIDByName("io_latency_crypto_map"), make([]byte, 4))
	if err != nil {
		return fmt.Errorf("read blk-crypto probe result: %w", err)
	}
	if len(value) != 8 {
		return fmt.Errorf("blk-crypto probe result has %d bytes, want 8", len(value))
	}
	result := int64(netutil.NativeEndian.Uint64(value))
	switch result {
	case int64(abi.IOLatencyCryptoUnused):
		return nil
	case int64(abi.IOLatencyCryptoInitialized):
		return fmt.Errorf("%w: iolatency histograms are incompatible with initialized software blk-crypto fallback",
			types.ErrTracingStopped)
	case int64(abi.IOLatencyCryptoPending):
		return fmt.Errorf("blk-crypto data probe did not execute")
	default:
		if result < 0 {
			return fmt.Errorf("read tfms_inited: %w", unix.Errno(-result))
		}
		return fmt.Errorf("invalid blk-crypto probe result %d", result)
	}
}
