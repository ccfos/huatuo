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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	testVariantTarget = "tcp_v4_rcv"
	testKprobeProgram = "tcp_v4_rcv_prog"
	testFentryProgram = "tcp_v4_rcv_fentry_prog"
)

var (
	errAttemptFailed  = errors.New("attempt failed")
	errRetryFailed    = errors.New("retry failed")
	errProbeTransient = errors.New("probe failed transiently")
)

// newTracingTestSpec mimics the pilot object: one hook with both entry points
// plus the tracepoint programs that must survive pruning.
func newTracingTestSpec() *ebpf.CollectionSpec {
	spec := &ebpf.CollectionSpec{
		Programs: map[string]*ebpf.ProgramSpec{
			"netif_receive_skb_prog": {
				Name:        "netif_receive_skb_prog",
				Type:        ebpf.TracePoint,
				SectionName: "tracepoint/net/netif_receive_skb",
			},
			"skb_copy_datagram_iovec_prog": {
				Name:        "skb_copy_datagram_iovec_prog",
				Type:        ebpf.TracePoint,
				SectionName: "tracepoint/skb/skb_copy_datagram_iovec",
			},
			testKprobeProgram: {
				Name:        testKprobeProgram,
				Type:        ebpf.Kprobe,
				SectionName: "kprobe/" + testVariantTarget,
			},
			testFentryProgram: {
				Name:        testFentryProgram,
				Type:        ebpf.Tracing,
				AttachType:  ebpf.AttachTraceFEntry,
				AttachTo:    testVariantTarget,
				SectionName: "fentry/" + testVariantTarget,
			},
		},
		Maps: map[string]*ebpf.MapSpec{
			"net_recv_lat_event_map": {
				Name:       "net_recv_lat_event_map",
				Type:       ebpf.PerfEventArray,
				KeySize:    4,
				ValueSize:  4,
				MaxEntries: 1,
			},
		},
	}

	return spec
}

func testTracingPair() TracingVariantPair {
	return TracingVariantPair{
		Kprobe: testKprobeProgram,
		Fentry: testFentryProgram,
		Target: testVariantTarget,
	}
}

// probeResult returns a probe reporting err for every target.
func probeResult(err error) tracingProbe {
	return func(string) error { return err }
}

// staticReader is a PerfEventReader stub; the selection logic only moves the
// reader around.
type staticReader struct {
	closed bool
}

func (r *staticReader) ReadInto(any) error { return nil }

func (r *staticReader) ReadBatch(func() any) (PerfEventBatch, error) {
	return PerfEventBatch{}, nil
}

func (r *staticReader) Close() error {
	r.closed = true
	return nil
}

// recordedAttempt records the spec of every attempt and returns the configured
// outcome.
type recordedAttempt struct {
	specs []*ebpf.CollectionSpec
	errs  []error
}

func (r *recordedAttempt) run(ctx context.Context, spec *ebpf.CollectionSpec) (BPF, PerfEventReader, error) {
	r.specs = append(r.specs, spec)

	index := len(r.specs) - 1
	if index < len(r.errs) && r.errs[index] != nil {
		return nil, nil, r.errs[index]
	}

	return new(defaultBPF), new(staticReader), nil
}

func (r *recordedAttempt) programs(index int) map[string]struct{} {
	names := make(map[string]struct{}, len(r.specs[index].Programs))
	for name := range r.specs[index].Programs {
		names[name] = struct{}{}
	}

	return names
}

func TestSelectTracingVariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		probe      tracingProbe
		wantMode   tracingMode
		wantReason bool
	}{
		{
			name:     "supported target prefers fentry",
			probe:    probeResult(nil),
			wantMode: tracingModeFentry,
		},
		{
			name:       "unsupported target selects kprobe",
			probe:      probeResult(errTracingTargetUnsupported),
			wantMode:   tracingModeKprobe,
			wantReason: true,
		},
		{
			name:     "transient probe failure still tries fentry",
			probe:    probeResult(errProbeTransient),
			wantMode: tracingModeFentry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mode, reason := selectTracingVariant([]TracingVariantPair{testTracingPair()}, tt.probe)
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
			if (reason != "") != tt.wantReason {
				t.Errorf("reason = %q, want non-empty: %t", reason, tt.wantReason)
			}
		})
	}
}

func TestPruneTracingVariantsKeepsSelectedEntryPoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        tracingMode
		wantPresent string
		wantDropped string
	}{
		{
			name:        "fentry keeps fentry only",
			mode:        tracingModeFentry,
			wantPresent: testFentryProgram,
			wantDropped: testKprobeProgram,
		},
		{
			name:        "kprobe keeps kprobe only",
			mode:        tracingModeKprobe,
			wantPresent: testKprobeProgram,
			wantDropped: testFentryProgram,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := newTracingTestSpec()
			if err := pruneTracingVariants(spec, []TracingVariantPair{testTracingPair()}, tt.mode); err != nil {
				t.Fatalf("pruneTracingVariants() error = %v", err)
			}

			if _, ok := spec.Programs[tt.wantPresent]; !ok {
				t.Errorf("program %q was pruned, want kept", tt.wantPresent)
			}
			if _, ok := spec.Programs[tt.wantDropped]; ok {
				t.Errorf("program %q was kept, want pruned", tt.wantDropped)
			}

			for _, name := range []string{"netif_receive_skb_prog", "skb_copy_datagram_iovec_prog"} {
				if _, ok := spec.Programs[name]; !ok {
					t.Errorf("tracepoint program %q was pruned, want kept", name)
				}
			}
			if _, ok := spec.Maps["net_recv_lat_event_map"]; !ok {
				t.Error("map net_recv_lat_event_map was pruned, want kept")
			}
		})
	}
}

func TestPruneTracingVariantsRejectsInvalidPairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pair TracingVariantPair
	}{
		{
			name: "missing kprobe program",
			pair: TracingVariantPair{Kprobe: "absent", Fentry: testFentryProgram, Target: testVariantTarget},
		},
		{
			name: "missing fentry program",
			pair: TracingVariantPair{Kprobe: testKprobeProgram, Fentry: "absent", Target: testVariantTarget},
		},
		{
			name: "kprobe hooks another target",
			pair: TracingVariantPair{Kprobe: testKprobeProgram, Fentry: testFentryProgram, Target: "udp_rcv"},
		},
		{
			name: "empty target",
			pair: TracingVariantPair{Kprobe: testKprobeProgram, Fentry: testFentryProgram},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := newTracingTestSpec()
			if err := pruneTracingVariants(spec, []TracingVariantPair{tt.pair}, tracingModeFentry); err == nil {
				t.Fatal("pruneTracingVariants() error = nil, want non-nil")
			}
		})
	}
}

func TestLoadTracingVariantWithFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		probe         tracingProbe
		errs          []error
		wantAttempts  int
		wantMode      tracingMode
		wantErr       bool
		wantBothErrs  bool
		wantCleanup   bool
		wantProgram   string
		wantNoProgram string
	}{
		{
			name:          "fentry succeeds without retry",
			probe:         probeResult(nil),
			wantAttempts:  1,
			wantMode:      tracingModeFentry,
			wantProgram:   testFentryProgram,
			wantNoProgram: testKprobeProgram,
		},
		{
			name:          "fentry failure retries kprobe",
			probe:         probeResult(nil),
			errs:          []error{errAttemptFailed, nil},
			wantAttempts:  2,
			wantMode:      tracingModeKprobe,
			wantProgram:   testKprobeProgram,
			wantNoProgram: testFentryProgram,
		},
		{
			name:         "both entry points fail",
			probe:        probeResult(nil),
			errs:         []error{errAttemptFailed, errRetryFailed},
			wantAttempts: 2,
			wantErr:      true,
			wantBothErrs: true,
		},
		{
			name:         "cleanup failure stops the fallback",
			probe:        probeResult(nil),
			errs:         []error{markTracingCleanupFailure(errAttemptFailed), nil},
			wantAttempts: 1,
			wantErr:      true,
			wantCleanup:  true,
		},
		{
			name:          "unsupported target skips the fentry load",
			probe:         probeResult(errTracingTargetUnsupported),
			wantAttempts:  1,
			wantMode:      tracingModeKprobe,
			wantProgram:   testKprobeProgram,
			wantNoProgram: testFentryProgram,
		},
		{
			name:         "unsupported target without kprobe fallback",
			probe:        probeResult(errTracingTargetUnsupported),
			errs:         []error{errAttemptFailed},
			wantAttempts: 1,
			wantErr:      true,
		},
		{
			name:          "transient probe failure keeps fentry first",
			probe:         probeResult(errProbeTransient),
			wantAttempts:  1,
			wantMode:      tracingModeFentry,
			wantProgram:   testFentryProgram,
			wantNoProgram: testKprobeProgram,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pristine := newTracingTestSpec()
			snapshot := pristine.Copy()

			attempt := &recordedAttempt{errs: tt.errs}
			object, reader, selection, err := loadTracingVariantWithFallback(
				t.Context(),
				pristine,
				[]TracingVariantPair{testTracingPair()},
				tt.probe,
				attempt.run,
			)

			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.wantBothErrs && (!errors.Is(err, errAttemptFailed) || !errors.Is(err, errRetryFailed)) {
				t.Errorf("error = %v, want both attempt and retry errors", err)
			}
			if tt.wantCleanup && !isTracingCleanupFailure(err) {
				t.Errorf("error = %v, want a cleanup failure", err)
			}
			if len(attempt.specs) != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(attempt.specs), tt.wantAttempts)
			}
			if len(attempt.specs) > 2 {
				t.Errorf("attempts = %d, want at most 2", len(attempt.specs))
			}
			if tt.wantErr {
				if object != nil || reader != nil {
					t.Error("failed load returned an object or reader, want nil")
				}
				return
			}

			if object == nil || reader == nil {
				t.Fatal("load returned a nil object or reader")
			}
			if selection.Mode != tt.wantMode {
				t.Errorf("selection mode = %q, want %q", selection.Mode, tt.wantMode)
			}

			programs := attempt.programs(len(attempt.specs) - 1)
			if _, ok := programs[tt.wantProgram]; !ok {
				t.Errorf("last attempt did not load %q", tt.wantProgram)
			}
			if _, ok := programs[tt.wantNoProgram]; ok {
				t.Errorf("last attempt loaded %q, want pruned", tt.wantNoProgram)
			}

			// The caller's spec must stay untouched for the next attempt and
			// for callers that load it themselves. Both sides of the
			// comparison are copies so that what Copy itself normalizes (nil
			// versus empty instruction slices) cannot mask a real change.
			if !reflect.DeepEqual(snapshot, pristine.Copy()) {
				t.Error("the caller's collection spec was modified")
			}
		})
	}
}

// TestLoadAttachAndEventPipeAttachesAndCreatesReader checks the pilot helper's
// contract against a real kernel: the returned object is already attached and
// the reader already exists, so the caller must not attach again.
func TestLoadAttachAndEventPipeAttachesAndCreatesReader(t *testing.T) {
	requireBPFPermission(t)

	spec := loadMinimalSpec(t)

	object, reader, err := loadAttachAndEventPipe(
		t.Context(), "test_minimal.elf", spec, nil, "events", 4096,
	)
	if err != nil {
		if errors.Is(err, ebpf.ErrNotSupported) ||
			errors.Is(err, unix.EPERM) ||
			errors.Is(err, unix.EACCES) {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("loadAttachAndEventPipe() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		_ = reader.Close()
		_ = object.Close()
	})

	inner, ok := object.(*defaultBPF)
	require.True(t, ok, "expected *defaultBPF, got %T", object)

	links := 0
	for _, program := range inner.programsByID {
		links += len(program.links)
	}
	require.Positive(t, links, "the returned object must already be attached")

	// The reader is live and closing it must not disturb the object.
	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close())
}

// pilotEntryPointObject points DefaultObjDir at the pilot object built with
// both entry points and returns its name and spec. The spec is the caller's
// baseline: nothing in the load path may modify it.
func pilotEntryPointObject(t *testing.T) (string, *ebpf.CollectionSpec) {
	t.Helper()

	objDir := filepath.Join("..", "..", "bpf")
	objName := "net_rx_latency_fentry.o"
	if _, err := os.Stat(filepath.Join(objDir, objName)); err != nil {
		t.Skipf("skipping: %v (run 'make gen-build' first)", err)
	}

	old := DefaultObjDir
	DefaultObjDir = objDir
	t.Cleanup(func() { DefaultObjDir = old })

	spec, err := loadCollectionSpec(objName)
	require.NoError(t, err)

	return objName, spec
}

// pilotObjectCounts counts the loaded programs and their links.
func pilotObjectCounts(t *testing.T, object BPF) (map[string]bool, int) {
	t.Helper()

	inner, ok := object.(*defaultBPF)
	require.True(t, ok, "expected *defaultBPF, got %T", object)

	loaded := make(map[string]bool, len(inner.programsByID))
	links := 0

	for _, program := range inner.programsByID {
		loaded[program.name] = true
		links += len(program.links)
	}

	return loaded, links
}

func skipUnsupportedLoad(t *testing.T, err error) {
	t.Helper()

	if errors.Is(err, ebpf.ErrNotSupported) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES) {
		t.Skipf("skipping: %v", err)
	}
}

// TestLoadAttachAndEventPipeWithFallbackSelectsPilotEntryPoint loads the pilot
// object against the running kernel and checks the invariants that hold on
// every kernel: exactly one tcp_v4_rcv entry point is loaded, the tracepoints
// survive the selection, and the object comes back attached.
func TestLoadAttachAndEventPipeWithFallbackSelectsPilotEntryPoint(t *testing.T) {
	requireBPFPermission(t)

	objName, _ := pilotEntryPointObject(t)

	object, reader, err := LoadAttachAndEventPipeWithFallback(
		t.Context(),
		objName,
		pilotObjectConstants(),
		[]TracingVariantPair{testTracingPair()},
		"net_recv_lat_event_map",
		DefaultPerfEventBufferBytes,
	)
	if err != nil {
		skipUnsupportedLoad(t, err)
		t.Fatalf("LoadAttachAndEventPipeWithFallback() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		_ = reader.Close()
		_ = object.Close()
	})

	loaded, links := pilotObjectCounts(t, object)
	require.NotEqual(t, loaded[testFentryProgram], loaded[testKprobeProgram],
		"exactly one entry point must be loaded")
	require.True(t, loaded["netif_receive_skb_prog"], "tracepoint programs must survive the selection")
	require.True(t, loaded["skb_copy_datagram_iovec_prog"], "tracepoint programs must survive the selection")
	require.Positive(t, links, "the returned object must already be attached")

	// The selection must agree with what the kernel reports for the target.
	wantFentry := probeFentryTarget(testVariantTarget) == nil
	if loaded[testFentryProgram] != wantFentry {
		t.Errorf("fentry loaded = %t, want %t for this kernel", loaded[testFentryProgram], wantFentry)
	}

	t.Logf("kernel selected the kprobe entry point: %t", loaded[testKprobeProgram])
}

// TestLoadTracingVariantForcesKprobeEntryPoint exercises the fallback that
// kernels without fentry support take, on a kernel that does support it.
func TestLoadTracingVariantForcesKprobeEntryPoint(t *testing.T) {
	requireBPFPermission(t)

	objName, spec := pilotEntryPointObject(t)
	attempt := func(ctx context.Context, spec *ebpf.CollectionSpec) (BPF, PerfEventReader, error) {
		return loadAttachAndEventPipe(
			ctx, objName, spec, pilotObjectConstants(), "net_recv_lat_event_map", DefaultPerfEventBufferBytes,
		)
	}

	object, reader, selection, err := loadTracingVariantWithFallback(
		t.Context(),
		spec,
		[]TracingVariantPair{testTracingPair()},
		probeResult(errTracingTargetUnsupported),
		attempt,
	)
	if err != nil {
		skipUnsupportedLoad(t, err)
		t.Fatalf("loadTracingVariantWithFallback() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		_ = reader.Close()
		_ = object.Close()
	})

	require.Equal(t, tracingModeKprobe, selection.Mode)
	require.NotEmpty(t, selection.Reason, "the fallback reason must be reported")

	loaded, links := pilotObjectCounts(t, object)
	require.True(t, loaded[testKprobeProgram], "the kprobe entry point must be loaded")
	require.False(t, loaded[testFentryProgram], "the fentry entry point must not be loaded")
	require.Positive(t, links, "the returned object must already be attached")
}

// pilotObjectConstants are the constants the tracer rewrites, with the values
// the integration test configuration uses.
func pilotObjectConstants() map[string]any {
	return map[string]any{
		"mono_wall_offset":      int64(0),
		"rxlat_thresh_netif":    int64(5 * 1000 * 1000),
		"rxlat_thresh_tcpv4":    int64(10 * 1000 * 1000),
		"rxlat_thresh_usercopy": int64(115 * 1000 * 1000),
	}
}

// TestLoadAttachAndEventPipeWithFallbackRejectsUnknownPair checks that a pair
// which the object does not carry stops the load instead of guessing an entry
// point.
func TestLoadAttachAndEventPipeWithFallbackRejectsUnknownPair(t *testing.T) {
	objBytes := loadMinimalObjBytes(t)

	old := DefaultObjDir
	DefaultObjDir = t.TempDir()
	t.Cleanup(func() { DefaultObjDir = old })

	require.NoError(t, os.WriteFile(
		filepath.Join(DefaultObjDir, "test_minimal.elf"), objBytes, 0o600,
	))

	object, reader, err := LoadAttachAndEventPipeWithFallback(
		t.Context(),
		"test_minimal.elf",
		nil,
		[]TracingVariantPair{{
			Kprobe: "test_kprobe",
			Fentry: "test_fentry",
			Target: "sys_openat",
		}},
		"events",
		4096,
	)
	if err == nil {
		t.Fatal("error = nil, want the missing fentry program to be reported")
	}
	if object != nil || reader != nil {
		t.Error("failed load returned an object or reader, want nil")
	}
	if strings.Contains(err.Error(), "unsupported BPF program type") {
		t.Errorf("error = %v, want a pairing error", err)
	}
}

func TestLoadTracingVariantWithFallbackRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	attempt := (&recordedAttempt{}).run

	tests := []struct {
		name       string
		spec       *ebpf.CollectionSpec
		pairs      []TracingVariantPair
		attemptFns []tracingAttempt
	}{
		{
			name:       "nil spec",
			pairs:      []TracingVariantPair{testTracingPair()},
			attemptFns: []tracingAttempt{attempt},
		},
		{
			name:       "no pairs",
			spec:       newTracingTestSpec(),
			attemptFns: []tracingAttempt{attempt},
		},
		{
			name:  "no attempt",
			spec:  newTracingTestSpec(),
			pairs: []TracingVariantPair{testTracingPair()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var attemptFn tracingAttempt
			if len(tt.attemptFns) > 0 {
				attemptFn = tt.attemptFns[0]
			}

			object, reader, _, err := loadTracingVariantWithFallback(
				context.Background(),
				tt.spec,
				tt.pairs,
				probeResult(nil),
				attemptFn,
			)
			if err == nil {
				t.Fatal("error = nil, want non-nil")
			}
			if object != nil || reader != nil {
				t.Error("invalid input returned an object or reader, want nil")
			}
		})
	}
}
