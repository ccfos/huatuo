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
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf"
	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
)

// irqTracingBPFObjectPath is the committed BPF object next to bpf/irqtracing.c.
// The test package runs with cmd/irqtracing as its working directory, so the
// repository bpf dir is two levels up.
const irqTracingBPFObjectPath = "../../bpf/irqtracing.o"

// loadTestBPF reads the compiled irqtracing.o and loads it with the given
// constants. Tests that need a real kernel skip when the object is missing
// from the tree (e.g. builds without a BPF toolchain).
func loadTestBPF(t *testing.T, consts map[string]any) (bpf.BPF, error) {
	t.Helper()

	raw, err := os.ReadFile(irqTracingBPFObjectPath)
	if err != nil {
		return nil, err
	}
	return bpf.LoadBPFFromBytes("irqtracing_test.o", raw, consts)
}

// requireBPFPermission skips the test when the process lacks BPF
// capabilities, so unprivileged containers keep passing the package tests.
func requireBPFPermission(t *testing.T) {
	t.Helper()

	if err := bpf.Init(nil); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("cannot initialize bpf resources: %v", err)
		}
		t.Fatalf("bpf.Init() = %v, want nil", err)
	}
	t.Cleanup(bpf.Shutdown)

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
func allowedTestCPUs(t *testing.T) []int {
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
	return cpus
}

func testTargetCPU(t *testing.T) int {
	t.Helper()

	cpus := allowedTestCPUs(t)

	for _, c := range cpus {
		if c != 0 {
			return c
		}
	}
	return cpus[0]
}

func readPerCPURateCounts(obj bpf.BPF, mapName string) ([]uint32, error) {
	const (
		stateSize   = 16
		countOffset = 8
	)

	items, err := obj.DumpMapByName(mapName)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("%s entries = %d, want 1", mapName, len(items))
	}
	possibleCPUs, err := ebpf.PossibleCPU()
	if err != nil {
		return nil, err
	}
	if len(items[0].Value) != possibleCPUs*stateSize {
		return nil, fmt.Errorf("%s value size = %d, want %d", mapName, len(items[0].Value), possibleCPUs*stateSize)
	}

	counts := make([]uint32, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu*stateSize + countOffset
		counts[cpu] = binary.NativeEndian.Uint32(items[0].Value[offset:])
	}
	return counts, nil
}

func floodUDPOnCPUs(t *testing.T, cpus []int, packets int) {
	t.Helper()

	conns := make([]net.Conn, len(cpus))
	for i := range conns {
		conns[i] = openUDPStimulus(t)
	}

	ready := make(chan error, len(cpus))
	start := make(chan struct{})
	done := make(chan error, len(cpus))
	for i, cpu := range cpus {
		go func(i, cpu int) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			var originalMask unix.CPUSet
			if err := unix.SchedGetaffinity(0, &originalMask); err != nil {
				ready <- err
				done <- nil
				return
			}

			var mask unix.CPUSet
			mask.Set(cpu)
			err := unix.SchedSetaffinity(0, &mask)
			ready <- err
			if err != nil {
				done <- nil
				return
			}

			<-start
			var workerErr error
			for range packets {
				if _, err := conns[i].Write([]byte("x")); err != nil {
					workerErr = fmt.Errorf("write UDP on cpu %d: %w", cpu, err)
					break
				}
			}
			if err := unix.SchedSetaffinity(0, &originalMask); err != nil && workerErr == nil {
				workerErr = fmt.Errorf("restore cpu affinity: %w", err)
			}
			done <- workerErr
		}(i, cpu)
	}

	var affinityErr error
	for range cpus {
		if err := <-ready; err != nil && affinityErr == nil {
			affinityErr = err
		}
	}
	close(start)
	for range cpus {
		require.NoError(t, <-done)
	}
	if affinityErr != nil {
		t.Skipf("cannot pin UDP workers: %v", affinityErr)
	}
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

func openUDPStimulus(t *testing.T) net.Conn {
	t.Helper()

	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	conn, err := net.Dial("udp4", listener.LocalAddr().String())
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64)
		for {
			if _, _, err := listener.ReadFrom(buf); err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = conn.Close()
		_ = listener.Close()
		<-done
	})
	return conn
}

func writeUDPPackets(t *testing.T, conn net.Conn, count int) {
	t.Helper()
	for range count {
		_, err := conn.Write([]byte("x"))
		require.NoError(t, err)
	}
}

func TestAllCPUsCollectSoftirqSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux")
	}
	requireBPFPermission(t)

	obj, err := loadTestBPF(t, map[string]any{"target_cpu": allCPUsTarget})
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

	if err := obj.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
	}); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("skipping: attach: %v", err)
		}
		require.NoError(t, err)
	}

	conn := openUDPStimulus(t)
	writeUDPPackets(t, conn, 64)

	assert.Eventually(t, func() bool {
		items, err := obj.DumpMapByName("source_counts")
		return err == nil && len(items) > 0
	}, 5*time.Second, 50*time.Millisecond,
		"target_cpu=-1 must admit softirq sources")

	nmissed, err := readDroppedSamples(obj)
	require.NoError(t, err)
	assert.Zero(t, nmissed, "omitting the runtime rate limit must leave collection unlimited")
}

func TestConfiguredRateLimitDropsSoftirqSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux")
	}
	requireBPFPermission(t)

	// A total budget of two gives source and victim one event per second each.
	obj, err := loadTestBPF(t, irqTracingBPFConstants(allCPUsTarget, 2))
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

	if err := obj.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
	}); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("skipping: attach: %v", err)
		}
		require.NoError(t, err)
	}

	conn := openUDPStimulus(t)
	writeUDPPackets(t, conn, 64)

	assert.Eventually(t, func() bool {
		nmissed, err := readDroppedSamples(obj)
		return err == nil && nmissed > 0
	}, 5*time.Second, 50*time.Millisecond,
		"configured source budget must reject excess softirq raises")
}

func TestPerCPURateLimitUnderContention(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux")
	}
	requireBPFPermission(t)

	cpus := allowedTestCPUs(t)
	if len(cpus) < 2 {
		t.Skip("requires at least two allowed CPUs")
	}
	if len(cpus) > 4 {
		cpus = cpus[:4]
	}

	const perStreamBudget = uint32(2)
	obj, err := loadTestBPF(t, irqTracingBPFConstants(allCPUsTarget, 2*uint64(perStreamBudget)))
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

	if err := obj.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
		{ProgramName: "probe_softirq_entry", Symbol: "irq/softirq_entry"},
	}); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("skipping: attach: %v", err)
		}
		require.NoError(t, err)
	}

	floodUDPOnCPUs(t, cpus, 64)

	activeCPUs := func(counts []uint32) int {
		active := 0
		for _, cpu := range cpus {
			if counts[cpu] > 0 {
				active++
			}
		}
		return active
	}
	var sourceCounts, victimCounts []uint32
	require.Eventually(t, func() bool {
		sourceCounts, err = readPerCPURateCounts(obj, "source_rlimit")
		if err != nil {
			return false
		}
		victimCounts, err = readPerCPURateCounts(obj, "victim_rlimit")
		return err == nil && activeCPUs(sourceCounts) >= 2 && activeCPUs(victimCounts) >= 2
	}, 5*time.Second, 50*time.Millisecond, "source and victim limiters must run on multiple CPUs")

	for cpu, count := range sourceCounts {
		if count > perStreamBudget {
			t.Errorf("source cpu %d admitted %d samples, want at most %d", cpu, count, perStreamBudget)
		}
	}
	for cpu, count := range victimCounts {
		if count > perStreamBudget {
			t.Errorf("victim cpu %d admitted %d samples, want at most %d", cpu, count, perStreamBudget)
		}
	}
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

	// Runtime rate-limit constants are intentionally omitted, which leaves the
	// limiter disabled. The full map is then the only possible drop source.
	obj, err := loadTestBPF(t, map[string]any{
		"target_cpu": int32(cpu),
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
	sourceMap, err := ebpf.NewMapFromID(ebpf.MapID(sourceMapID))
	require.NoError(t, err)
	defer sourceMap.Close()

	// Fill source_counts with 1024 synthetic keys that cannot collide with
	// real events: real pids are small and 0xF0000000+ never occurs.
	possibleCPUs, err := ebpf.PossibleCPU()
	require.NoError(t, err)
	perCPUValue := make([]uint64, possibleCPUs)
	for cpu := range perCPUValue {
		perCPUValue[cpu] = 1
	}
	for i := 0; i < 1024; i++ {
		err := sourceMap.Put(
			stackKeyBytes(&bpfabi.IrqtracingStackKey{
				PID: 0xF0000000 + uint32(i),
				Vec: 99,
			}),
			perCPUValue,
		)
		require.NoError(t, err)
	}

	if err := obj.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
	}); err != nil {
		if attachFailedForEnvironment(err) {
			t.Skipf("skipping: attach: %v", err)
		}
		require.NoError(t, err)
	}

	// Raise softirqs on the pinned cpu with loopback UDP writes. Every admitted
	// sample now fails to insert into the full source_counts map and must be
	// counted in dropped_samples.
	conn := openUDPStimulus(t)
	writeUDPPackets(t, conn, 64)

	assert.Eventually(t, func() bool {
		nmissed, err := readDroppedSamples(obj)
		return err == nil && nmissed > 0
	}, 5*time.Second, 50*time.Millisecond, "map-full insertion failures must be counted in dropped_samples")
}
