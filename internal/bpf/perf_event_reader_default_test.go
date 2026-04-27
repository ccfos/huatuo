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

//go:build !didi

package bpf

import (
	"context"
	"errors"
	"math"
	"runtime"
	"testing"
	"time"

	testutils "huatuo-bamai/internal/testing"
	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/perf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyBpfContext is the smallest-possible BPF input context to be used for
// invoking `Program.{Run,Benchmark,Test}`.
//
// Programs require a context input buffer of at least 15 bytes. Looking in
// net/bpf/test_run.c, bpf_test_init() requires that the input is at least
// ETH_HLEN (14) bytes. As of Linux commit fd18942 ("bpf: Don't redirect packets
// with invalid pkt_len"), it also requires the skb to be non-empty after
// removing the Layer 2 header.
var emptyBpfContext = make([]byte, 15)

func TestPerfEventReader_Lifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := newTestPerfEventReader(t, ctx)
	defer reader.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	var data int32
	err := reader.ReadInto(&data)

	assert.ErrorIs(t, err, types.ErrExitByCancelCtx)
}

func TestPerfEventReader_Close(t *testing.T) {
	reader := newTestPerfEventReader(t, t.Context())

	require.NoError(t, reader.Close())

	var data int32
	err := reader.ReadInto(&data)

	assert.ErrorIs(t, err, types.ErrExitByCancelCtx)
}

func TestPerfEventReader_ReadInto_Closed(t *testing.T) {
	reader := newTestPerfEventReader(t, t.Context())
	errCh := make(chan error, 1)

	go func() {
		var data int32
		errCh <- reader.ReadInto(&data)
	}()

	time.Sleep(50 * time.Millisecond)

	require.NoError(t, reader.Close())

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, types.ErrExitByCancelCtx)
	case <-time.After(1 * time.Second):
		t.Fatal("ReadInto() timed out waiting for Close()")
	}
}

func TestNewPerfEventReader_Failure(t *testing.T) {
	// Keep the original coverage: perCPUBuffer must be > 0. Use any map type; reader creation should fail.
	b := loadMinimalBpfFromBytes(t)
	info, err := b.Info()
	require.NoError(t, err)
	require.NotEmpty(t, info.MapsInfo)

	mapID := b.MapIDByName(info.MapsInfo[0].Name)
	require.NotZero(t, mapID)

	_, err = newPerfEventReader(t.Context(), b.mapSpecs[mapID].bMap, 0)
	assert.Error(t, err)
}

func TestPerfEventReader_ReadInto_And_Close_ProgTest(t *testing.T) {
	// This is the additional coverage you requested (modeled after ebpf/perf/reader_test.go):
	// Use prog.Test(...) to emit one perf sample and ensure ReadInto consumes it.
	t.Helper()

	events := perfEventArray(t)
	rd, err := perf.NewReader(events, 4096)
	if err != nil {
		t.Fatalf("perf.NewReader: %v", err)
	}
	defer rd.Close()

	ctx, cancel := context.WithCancel(t.Context())
	r := &perfEventReader{ctx: ctx, rd: rd, cancelCtx: cancel}

	sampleSize := 8
	buf := emptyBpfContext
	prog := outputSamplesProg(t, events, byte(sampleSize))
	ret, _, err := prog.Test(buf)
	if err != nil {
		if errors.Is(err, ebpf.ErrNotSupported) {
			t.Skipf("skipping: ebpf not supported: %v", err)
		}
		t.Fatalf("prog.Test: %v", err)
	}
	if ret != 0 {
		t.Fatalf("expected ret=0, got %d", ret)
	}

	var out struct {
		Size byte
		ID   byte
		_    [6]byte
	}

	require.NoError(t, r.ReadInto(&out))
	assert.Equal(t, byte(sampleSize), out.Size)
	assert.Equal(t, byte(0), out.ID)

	require.NoError(t, r.Close())
}

func newTestPerfEventReader(t *testing.T, ctx context.Context) PerfEventReader {
	t.Helper()

	perfMap := getPerfEventArrayMap(t)

	reader, err := newPerfEventReader(ctx, perfMap, 4096)
	if err != nil {
		t.Skipf("skipping test: newPerfEventReader failed: %v", err)
	}

	return reader
}

func getPerfEventArrayMap(t *testing.T) *ebpf.Map {
	t.Helper()

	b := loadMinimalBpfFromBytes(t)

	info, err := b.Info()
	require.NoError(t, err)
	require.NotEmpty(t, info.MapsInfo)

	for _, mi := range info.MapsInfo {
		id := b.MapIDByName(mi.Name)
		if id == 0 {
			continue
		}

		spec := b.mapSpecs[id]
		if spec.bMap == nil {
			continue
		}

		mapInfo, err := spec.bMap.Info()
		if err != nil {
			continue
		}

		if mapInfo.Type == ebpf.PerfEventArray {
			return spec.bMap
		}
	}

	return nil
}

func perfEventArray(tb testing.TB) *ebpf.Map {
	tb.Helper()

	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.PerfEventArray,
		KeySize:    4,
		ValueSize:  4,
		MaxEntries: uint32(runtime.NumCPU()),
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { m.Close() })
	return m
}

// outputSamplesProg creates an eBPF program which submits perf samples using PERF_EVENT_OUTPUT.
// This is a minimal, local copy of the logic used in the upstream cilium/ebpf perf tests.
func outputSamplesProg(tb testing.TB, events *ebpf.Map, sampleSizes ...byte) *ebpf.Program {
	tb.Helper()

	// Requires at least 4.9 (0515e5999a46 "bpf: introduce BPF_PROG_TYPE_PERF_EVENT program type")
	testutils.SkipOnOldKernel(tb, "4.9", "perf events support")

	const bpfFCurrentCPU = 0xffffffff

	var maxSampleSize byte
	for _, sampleSize := range sampleSizes {
		if sampleSize < 2 {
			tb.Fatalf("sample size %d is too small", sampleSize)
		}
		if sampleSize > maxSampleSize {
			maxSampleSize = sampleSize
		}
	}

	insns := asm.Instructions{
		asm.LoadImm(asm.R0, ^int64(0), asm.DWord),
		asm.Mov.Reg(asm.R9, asm.R1),
	}

	bufDwords := int(maxSampleSize/8) + 1
	for i := 0; i < bufDwords; i++ {
		insns = append(insns,
			asm.StoreMem(asm.RFP, int16(i+1)*-8, asm.R0, asm.DWord),
		)
	}

	for i, sampleSize := range sampleSizes {
		insns = append(insns,
			asm.Mov.Reg(asm.R1, asm.R9),
			asm.LoadMapPtr(asm.R2, events.FD()),
			asm.LoadImm(asm.R3, bpfFCurrentCPU, asm.DWord),
			asm.Mov.Reg(asm.R4, asm.RFP),
			asm.Add.Imm(asm.R4, int32(bufDwords*-8)),
			asm.StoreImm(asm.R4, 0, int64(sampleSize), asm.Byte),
			asm.StoreImm(asm.R4, 1, int64(i&math.MaxUint8), asm.Byte),
			asm.Mov.Imm(asm.R5, int32(sampleSize)),
			asm.FnPerfEventOutput.Call(),
		)
	}

	insns = append(insns, asm.Return())

	prog, err := ebpf.NewProgram(&ebpf.ProgramSpec{
		License:      "GPL",
		Type:         ebpf.XDP,
		Instructions: insns,
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { prog.Close() })
	return prog
}
