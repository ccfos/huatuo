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

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf"
)

// bpfRatelimitCfg mirrors struct bpf_ratelimit_event in
// bpf/include/abi/bpf_ratelimit_types.h so the test can rewrite the limiter
// config constants.
type bpfRatelimitCfg struct {
	IntervalNS     uint64
	WindowStartNS  uint64
	Burst          uint64
	MaxBurst       uint64
	EventsInWindow uint64
	MissedInWindow uint64
	TotalEvents    uint64
	TotalMissed    uint64
	TotalElapsedNS uint64
}

// Compile-time layout guard: struct bpf_ratelimit_event is 9 x u64 = 72 bytes.
var _ = [1]struct{}{}[72-unsafe.Sizeof(bpfRatelimitCfg{})]

// irqTracingBPFObjectPath is the committed BPF object next to bpf/irq_tracing.c.
// The test package runs with cmd/irqtracing as its working directory, so the
// repository bpf dir is two levels up.
const irqTracingBPFObjectPath = "../../bpf/irq_tracing.o"

// loadTestBPF reads the compiled irq_tracing.o and loads it with the given
// constants. Tests that need a real kernel skip when the object is missing
// from the tree (e.g. builds without a BPF toolchain).
func loadTestBPF(t *testing.T, consts map[string]any) (bpf.BPF, error) {
	t.Helper()

	raw, err := os.ReadFile(irqTracingBPFObjectPath)
	if err != nil {
		return nil, err
	}
	return bpf.LoadBPFFromBytes("irq_tracing_test.o", raw, consts)
}

// requireBPFPermission skips the test when the process lacks BPF
// capabilities, so unprivileged containers keep passing the package tests.
func requireBPFPermission(t *testing.T) {
	t.Helper()

	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.Hash,
		KeySize:    4,
		ValueSize:  8,
		MaxEntries: 1,
	})
	if err != nil {
		if errors.Is(err, ebpf.ErrNotSupported) ||
			errors.Is(err, unix.EPERM) ||
			errors.Is(err, unix.EACCES) {
			t.Skipf("insufficient permissions for bpf: %v", err)
		}
		t.Fatalf("ebpf.NewMap() = %v, want nil", err)
	}
	_ = m.Close()
}

// testTargetCPU picks a cpu the test process is allowed to run on,
// preferring a non-zero one (cpu 0 often runs container housekeeping).
func testTargetCPU(t *testing.T) int {
	t.Helper()

	data, err := os.ReadFile("/proc/self/status")
	require.NoError(t, err)

	line := ""
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "Cpus_allowed_list:") {
			line = strings.TrimSpace(strings.TrimPrefix(l, "Cpus_allowed_list:"))
			break
		}
	}
	require.NotEmpty(t, line, "no Cpus_allowed_list in /proc/self/status")

	var cpus []int
	for _, part := range strings.Split(line, ",") {
		if strings.Contains(part, "-") {
			r := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(r[0])
			hi, err2 := strconv.Atoi(r[1])
			require.NoError(t, err1)
			require.NoError(t, err2)
			for c := lo; c <= hi; c++ {
				cpus = append(cpus, c)
			}
		} else {
			c, err := strconv.Atoi(part)
			require.NoError(t, err)
			cpus = append(cpus, c)
		}
	}
	require.NotEmpty(t, cpus)

	for _, c := range cpus {
		if c != 0 {
			return c
		}
	}
	return cpus[0]
}

// attachFailedForEnvironment reports whether the attach error comes from a
// restricted environment (permissions, unsupported feature) rather than a
// real problem with the object. Only precisely identifiable errors skip:
// anything else, including EINVAL, must fail the test so object or attach
// regressions cannot hide behind a SKIP.
func attachFailedForEnvironment(err error) bool {
	return errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES) ||
		errors.Is(err, ebpf.ErrNotSupported)
}

// TestMapFullDropCounted locks the map-full regression on a real kernel:
// when a count map is full, an admitted sample whose insertion fails must
// increment dropped_samples, otherwise nmissed would claim a complete
// profile even though stacks are missing. Deleting the count_drop call in
// account_stack makes this test fail.
func TestMapFullDropCounted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux")
	}
	requireBPFPermission(t)

	cpu := testTargetCPU(t)

	// Pin this thread to the target cpu so the softirqs it raises fire the
	// probe on that cpu.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var mask unix.CPUSet
	mask.Set(cpu)
	if err := unix.SchedSetaffinity(0, &mask); err != nil {
		t.Skipf("skipping: cannot pin to cpu %d: %v", cpu, err)
	}

	// Disarm the limiter with an effectively unlimited burst so limiter drops
	// cannot occur: the full map is then the only possible drop source, and
	// deleting the count_drop call in account_stack makes this test fail.
	cfg := bpfRatelimitCfg{IntervalNS: 1e9, Burst: 1 << 60}
	obj, err := loadTestBPF(t, map[string]any{
		"target_cpu":                    uint32(cpu),
		"___bpf_rlimit_cfg_source_rate": cfg,
		"___bpf_rlimit_cfg_victim_rate": cfg,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("skipping: BPF object %s not built: %v", irqTracingBPFObjectPath, err)
		}
		if errors.Is(err, ebpf.ErrNotSupported) {
			t.Skipf("skipping: load bpf: %v", err)
		}
		require.NoError(t, err)
	}
	defer obj.Close()

	sourceMapID := obj.MapIDByName("source_counts")
	require.NotZero(t, sourceMapID)

	// Fill source_counts with 1024 synthetic keys that cannot collide with
	// real events: real pids are small and 0xF0000000+ never occurs.
	items := make([]bpf.MapItem, 0, 1024)
	for i := 0; i < 1024; i++ {
		val := make([]byte, 8)
		binary.LittleEndian.PutUint64(val, 1)
		items = append(items, bpf.MapItem{
			Key:   stackKeyBytes(&irqStackKey{Pid: 0xF0000000 + uint32(i), Vec: 99}),
			Value: val,
		})
	}
	require.NoError(t, obj.WriteMapItems(sourceMapID, items))

	if err := obj.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
	}); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("skipping: attach: %v", err)
		}
		require.NoError(t, err)
	}

	// Raise softirqs on the pinned cpu: NET_RX from UDP writes to a closed
	// loopback port plus the natural timer ticks. Every admitted sample now
	// fails to insert into the full source_counts map and must be counted in
	// dropped_samples.
	conn, err := net.Dial("udp", "127.0.0.1:9")
	if err == nil {
		defer conn.Close()
		for i := 0; i < 64; i++ {
			_, _ = conn.Write([]byte("x"))
		}
	}

	assert.Eventually(t, func() bool {
		nmissed, err := readDroppedSamples(obj)
		return err == nil && nmissed > 0
	}, 5*time.Second, 50*time.Millisecond, "map-full insertion failures must be counted in dropped_samples")
}
