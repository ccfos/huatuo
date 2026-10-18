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

// IOLatency kernel qualifications reuse production probes and owned or read-only test disks.
package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Live kernel argument qualification, selected by make test.

// TEST_INTEGRATION=true exercises the active backend against the live kernel.
func TestIOLatencyArgumentProbeKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to probe live block tracepoints")
	}
	ctx := ioControlKernelContext(t)
	if os.Getenv("TEST_IOLATENCY_IO_DEVICE") == "" && os.Getenv("TEST_IOLATENCY_DISK_CANDIDATES") == "" {
		disks, isOwned := prepareIOKernelDisks(t, ctx)
		t.Setenv("TEST_IOLATENCY_DISK_CANDIDATES", strings.Join(disks, " "))
		t.Setenv("TEST_IOLATENCY_IO_EXCLUSIVE", "")
		if isOwned {
			t.Setenv("TEST_IOLATENCY_IO_EXCLUSIVE", "1")
		}
	}
	if err := bpf.Init(&bpf.Option{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	constants, err := loadIOLatencyTracepointArguments()
	if err != nil {
		t.Fatal(err)
	}
	// Check the complete live ABI independently of the production selector.
	probe, err := bpf.LoadBPF("iolatency_probe.o", nil)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if err := probe.Close(); err != nil {
				t.Error(err)
			}
		}()
		for _, layout := range ioLatencyTestLayouts {
			require.NoError(t, ctx.Err())
			var count uint32
			for ; count <= layout.legacyArguments; count++ {
				available, err := probeIOLatencyRawArgument(probe, layout.symbol, count)
				if err != nil {
					t.Fatal(err)
				}
				if !available {
					break
				}
			}
			if count != layout.arguments && count != layout.legacyArguments {
				t.Fatalf("%s has unsupported argument count %d", layout.symbol, count)
			}
			var want uint32
			if count != layout.arguments {
				want = 1
			}
			assertIOLatencyConstant(t, constants, layout.constant, want)
		}
	}()
	for _, definition := range ioLatencyTracepoints {
		if definition.constant != "" {
			t.Logf("%s pointer argument: %v", definition.symbol, constants[definition.constant])
		} else {
			t.Logf("%s pointer argument: 0", definition.symbol)
		}
	}
	kernelConstants, err := loadIOLatencyKernelConstants()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range kernelConstants {
		constants[name] = value
	}
	require.NoError(t, ctx.Err())
	object, err := bpf.LoadBPF("iolatency_tracing.o", constants)
	if err != nil {
		var verifier *ebpf.VerifierError
		if errors.As(err, &verifier) {
			t.Logf("%-100v", verifier)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := object.Close(); err != nil {
			t.Error(err)
		}
	})
	session := &ioLatencySession{object: object}
	if err := attachIOLatencyHooks(object); err != nil {
		t.Fatal(err)
	}
	if err := session.discoverDisks(procfs.DefaultPathByType("sys")); err != nil {
		if errors.Is(err, types.ErrNotSupported) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	t.Run("read IO", func(t *testing.T) {
		runIOLatencyReadQualification(t, ctx, session)
	})
}

// Direct IO initializes Q unconditionally on the older timestamp layouts.
// Kernels with QUEUE_FLAG_BIO_ISSUE_TIME may legitimately leave it zero;
// native accounting fixtures separately cover initialized and missing Q.
func ioLatencyKernelRequiresQueueSamples(t *testing.T) bool {
	t.Helper()
	spec, err := btf.LoadKernelSpec()
	require.NoError(t, err)
	var bio *btf.Struct
	require.NoError(t, spec.TypeByName("bio", &bio))
	hasTime := false
	for _, member := range bio.Members {
		if member.Name == "bi_issue" || member.Name == "issue_time_ns" {
			hasTime = true
		}
	}
	iter := spec.Iterate()
	for iter.Next() {
		if enum, ok := iter.Type.(*btf.Enum); ok {
			for _, value := range enum.Values {
				if value.Name == "QUEUE_FLAG_BIO_ISSUE_TIME" {
					return false
				}
			}
		}
	}
	return hasTime
}

// Read real kernel data even on qualification kernels without blk-crypto.
// linux_banner supplies a nonzero byte; address zero exercises the helper's
// actual failed-read path. Neither case creates or injects block IO state.
func TestIOLatencyCryptoProbeKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to read live kernel data")
	}
	ctx := ioControlKernelContext(t)
	if err := bpf.Init(&bpf.Option{}); err != nil {
		t.Fatal(err)
	}
	defer bpf.Shutdown()
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	addresses, err := symbol.KsymbolSearchAddr([]string{"linux_banner"}, symbol.KsymbolLookupData)
	if err != nil {
		t.Fatal(err)
	}
	if addresses["linux_banner"].Start == 0 {
		t.Fatal("linux_banner is unavailable")
	}
	for _, test := range []struct {
		name    string
		address uint64
		want    error
	}{
		{name: "initialized byte", address: addresses["linux_banner"].Start, want: types.ErrTracingStopped},
		{name: "failed read", want: unix.EFAULT},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, ctx.Err())
			object, err := bpf.LoadBPF("iolatency_probe.o", map[string]any{
				"io_latency_tfms_inited":  test.address,
				"io_latency_crypto_modes": uint32(1),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := object.Close(); err != nil {
					t.Errorf("close kernel data probe: %v", err)
				}
			})
			if err := readIOLatencyCryptoProbe(object); !errors.Is(err, test.want) {
				t.Fatalf("kernel data probe = %v, want %v", err, test.want)
			}
		})
	}
}

// Real disk configuration and IO qualifications.

func runIOLatencyReadQualification(t *testing.T, parent context.Context, session *ioLatencySession) {
	t.Helper()
	device := os.Getenv("TEST_IOLATENCY_IO_DEVICE")
	candidates := strings.Fields(os.Getenv("TEST_IOLATENCY_DISK_CANDIDATES"))
	if len(candidates) != 0 {
		device = ""
	}
	// Reuse this session's discovery instead of loading a second probe for selection.
	for _, candidate := range candidates {
		for _, name := range session.devices {
			if name == candidate {
				device = filepath.Join("/dev", name)
				break
			}
		}
		if device != "" {
			break
		}
	}
	if device == "" {
		t.Skip("no eligible SCSI/NVMe/virtio disk available for read IO testing")
	}
	ctx, cancel := context.WithTimeout(parent, 210*time.Second)
	defer cancel()
	device, err := filepath.EvalSymlinks(device)
	require.NoError(t, err)
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(device, &stat))
	require.Equal(t, uint32(unix.S_IFBLK), stat.Mode&unix.S_IFMT,
		"TEST_IOLATENCY_IO_DEVICE must be an existing block device")
	major, minor := unix.Major(stat.Rdev), unix.Minor(stat.Rdev)
	if _, supported := session.devices[[2]uint32{major, minor}]; !supported {
		t.Skipf("IOLatency read IO: %s does not provide both request timestamps", device)
	}
	sysPath, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(sysPath, "partition"))
	require.ErrorIs(t, err, os.ErrNotExist, "qualification requires a whole disk, not a partition")

	drain := func() {
		t.Helper()
		// Shared disks may keep servicing unrelated IO after fio exits.
		// Only fixtures created by the test require full-device quiescence.
		if os.Getenv("TEST_IOLATENCY_IO_EXCLUSIVE") != "1" {
			return
		}
		drainCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		idle := 0
		for {
			require.NoError(t, drainCtx.Err(), "waiting for %s IO to drain", device)
			data, err := os.ReadFile(filepath.Join(sysPath, "inflight"))
			require.NoError(t, err)
			var reads, writes uint64
			n, err := fmt.Sscanf(string(data), "%d %d", &reads, &writes)
			require.NoError(t, err)
			require.Equal(t, 2, n)
			if reads == 0 && writes == 0 {
				idle++
			} else {
				idle = 0
			}
			if idle == 2 {
				return
			}
			select {
			case <-drainCtx.Done():
				t.Fatalf("waiting for %s IO to drain: %v", device, drainCtx.Err())
			case <-ticker.C:
			}
		}
	}
	snapshot := func(round int) ioLatencyCounters {
		t.Helper()
		require.NoError(t, ctx.Err())
		require.NoError(t, session.checkHealth())
		samples, err := session.captureHostLatency()
		require.NoError(t, err)
		require.NoError(t, session.checkHealth())
		require.NoError(t, ctx.Err())
		if sample := samples[ioLatencyHostKey{Major: major, Minor: minor}]; sample != nil {
			return *sample // REQ_OP_READ is zero.
		}
		return ioLatencyCounters{}
	}

	requireQueue := ioLatencyKernelRequiresQueueSamples(t)
	t.Logf("read-only qualification: device=%s (%d:%d), latency_bounds=%v, size_bounds=%v",
		device, major, minor, ioLatencyBucketLabels, ioSizeBucketLabels)
	t.Log("fio request sizes/counts do not establish bio-chain coverage; observe bi_next separately")
	drain()
	previous := snapshot(0)
	for round := 1; round <= 3; round++ {
		command := exec.CommandContext(ctx, "timeout", "--kill-after=5s", "30s", "fio",
			"--name=global", "--readonly", "--filename="+device, "--allow_file_create=0",
			"--ioengine=libaio", "--direct=1", "--size=128m", "--time_based=1",
			"--runtime=5", "--group_reporting=1", "--exitall_on_error=1",
			"--name=direct-read", "--rw=read", "--bs=4k", "--iodepth=1",
			"--name=large-read", "--stonewall", "--rw=read", "--bs=4m", "--iodepth=16",
			"--name=merge-read", "--stonewall", "--rw=read", "--bs=512", "--iodepth=256",
			"--iodepth_batch_submit=256", "--iodepth_batch_complete_min=1",
			"--iodepth_batch_complete_max=256", "--output-format=json")
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return unix.Kill(-command.Process.Pid, unix.SIGKILL) }
		command.WaitDelay = 5 * time.Second
		output, err := command.CombinedOutput()
		t.Logf("round=%d fio: %s", round, output)
		require.NoError(t, err, "read-only fio round %d", round)
		drain()
		current := snapshot(round)
		delta, monotonic := ioLatencyCountersDelta(&previous, &current)
		require.True(t, monotonic, "production counters decreased in round %d", round)
		for stage, definition := range ioLatencyStageMetrics {
			var count uint64
			for _, value := range delta.Buckets[stage] {
				count += value
			}
			t.Logf("round=%d device=%d:%d read stage=%s samples=%d buckets=%v",
				round, major, minor, definition.name, count, delta.Buckets[stage])
			if count == 0 && (stage == ioLatencyStageD2C || requireQueue) {
				t.Errorf("round %d: %s produced no read samples", round, definition.name)
			}
		}
		t.Logf("round=%d device=%d:%d read queued_size=%v issued_size=%v",
			round, major, minor, delta.QueuedSize, delta.IssuedSize)
		previous = current
	}
}
