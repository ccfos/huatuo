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

// Startup checks require an RCU-delayed hash allocator, select SCSI/NVMe
// drivers, and exclude initialized software blk-crypto fallback. Temporary
// data probes are unloaded before normal IO tracing.
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
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_kernel_probe.c -o $BPF_DIR/iolatency_kernel_probe.o

const (
	ioLatencyRootBlkcg = "io_latency_root_blkcg"

	// The startup probe reads one bool per mode. This capacity accommodates
	// the complete runtime enum; larger arrays fail the check explicitly.
	ioLatencyCryptoModeCapacity = 64
)

func loadIOLatencyKernelConstants() (map[string]any, bool, error) {
	btfPath := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
	spec, err := btf.LoadSpec(btfPath)
	if err != nil {
		return nil, false, fmt.Errorf("%w: iolatency: load kernel BTF %s: %w",
			types.ErrTracingStopped, btfPath, err)
	}
	if err := checkIOLatencyHashAllocator(spec); err != nil {
		return nil, false, err
	}
	addresses, err := symbol.KsymbolSearchAddresses(
		"tfms_inited", "blkcg_root", "blk_stat_disable_accounting",
	)
	if err != nil {
		return nil, false, fmt.Errorf("read iolatency kernel symbols: %w", err)
	}
	if address := addresses["tfms_inited"]; address != 0 {
		if err := checkIOLatencyCryptoFallback(spec, address); err != nil {
			return nil, false, err
		}
	} else {
		enabled, err := ioLatencyCryptoFallbackConfigured()
		if err != nil {
			return nil, false, err
		}
		if enabled {
			return nil, false, fmt.Errorf("iolatency: configured blk-crypto fallback has no visible tfms_inited symbol")
		}
	}
	return map[string]any{
		ioLatencyRootBlkcg: addresses["blkcg_root"],
	}, addresses["blk_stat_disable_accounting"] != 0, nil
}

// C may still read an inactive node after GC removes its key. The cache
// requires deletion to defer node reuse until that RCU reader exits.
// bpf_mem_alloc-backed HASH maps reuse nodes immediately and are excluded.
func checkIOLatencyHashAllocator(spec *btf.Spec) error {
	member, err := ioControlHashAllocatorMember(spec)
	if err != nil {
		return fmt.Errorf("%w: iolatency: %w",
			types.ErrTracingStopped, err)
	}
	if member != nil {
		return fmt.Errorf("%w: iolatency: bpf_htab.%s uses bpf_mem_alloc; inactive bio cache is not supported",
			types.ErrTracingStopped, member.Name)
	}
	return nil
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
		path := filepath.Join(filepath.Dir(procfs.DefaultPath()), "boot",
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
	object, err := bpf.LoadBPF("iolatency_kernel_probe.o", map[string]any{
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
		ProgramName: "probe_crypto_fallback", Symbol: "sys_enter",
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
	case 1:
		return nil
	case 2:
		return fmt.Errorf("%w: iolatency histograms are incompatible with initialized software blk-crypto fallback",
			types.ErrTracingStopped)
	case 0:
		return fmt.Errorf("blk-crypto data probe did not execute")
	default:
		if result < 0 {
			return fmt.Errorf("read tfms_inited: %w", unix.Errno(-result))
		}
		return fmt.Errorf("invalid blk-crypto probe result %d", result)
	}
}
