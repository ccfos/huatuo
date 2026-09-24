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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/features"
	"github.com/cilium/ebpf/link"
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
type staticReader struct{}

func (r *staticReader) ReadInto(any) error { return nil }

func (r *staticReader) ReadBatch(func() any) (PerfEventBatch, error) {
	return PerfEventBatch{}, nil
}

func (r *staticReader) Close() error { return nil }

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
			pairs := []TracingVariantPair{testTracingPair()}
			if err := pruneTracingVariants(spec, pairs, tracingVariantKeep(pairs, tt.mode)); err != nil {
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
			pairs := []TracingVariantPair{tt.pair}
			if err := pruneTracingVariants(spec, pairs, tracingVariantKeep(pairs, tracingModeFentry)); err == nil {
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

// pilotObjectCounts maps every loaded program to the number of links it holds,
// plus their total. A program is loaded when its key is present, so a caller
// can require both the program and the exact number of links on it: a hook that
// carries two links is attached twice. After Close the object no longer tracks
// any program, which is how a caller checks that it released its resources.
func pilotObjectCounts(t *testing.T, object BPF) (map[string]int, int) {
	t.Helper()

	inner, ok := object.(*defaultBPF)
	require.True(t, ok, "expected *defaultBPF, got %T", object)

	loaded := make(map[string]int, len(inner.programsByID))
	links := 0

	for _, program := range inner.programsByID {
		loaded[program.name] = len(program.links)
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

	// One hook, one link: two links on the loaded entry point would mean the
	// same function is traced twice.
	selected := testFentryProgram
	if _, ok := loaded[testKprobeProgram]; ok {
		selected = testKprobeProgram
	}
	require.Equal(t, 1, loaded[selected], "the selected entry point must carry exactly one link")

	// The pilot object keeps its two tracepoints, each attached once.
	require.Equal(t, 1, loaded["netif_receive_skb_prog"], "tracepoint programs must survive the selection")
	require.Equal(t, 1, loaded["skb_copy_datagram_iovec_prog"], "tracepoint programs must survive the selection")
	require.Positive(t, links, "the returned object must already be attached")

	// A successful probe does not promise a successful attach, so the
	// expectation comes from the attempt itself: fentry must be selected
	// exactly when loading it alone works on this kernel.
	wantFentry := fentryLoadSupported(t, objName)
	_, loadedFentry := loaded[testFentryProgram]
	if loadedFentry != wantFentry {
		t.Errorf("fentry loaded = %t, want %t for this kernel", loadedFentry, wantFentry)
	}

	_, loadedKprobe := loaded[testKprobeProgram]
	t.Logf("kernel selected the kprobe entry point: %t", loadedKprobe)
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
	require.Equal(t, 1, loaded[testKprobeProgram], "the kprobe entry point must carry exactly one link")
	require.NotContains(t, loaded, testFentryProgram, "the fentry entry point must not be loaded")
	require.Equal(t, 1, loaded["netif_receive_skb_prog"], "tracepoint programs must survive the selection")
	require.Equal(t, 1, loaded["skb_copy_datagram_iovec_prog"], "tracepoint programs must survive the selection")
	require.Positive(t, links, "the returned object must already be attached")
}

// fentryLoadSupported attempts the fentry entry point on its own and reports
// whether this kernel can attach it.
//
// The attempt is the verdict, not a probe: a probe succeeding does not promise
// a successful attach, and a failure that is not a missing kernel capability
// fails the test rather than being reported as "this kernel cannot".
func fentryLoadSupported(t *testing.T, objName string) bool {
	t.Helper()

	object, reader, err := LoadAttachAndEventPipeForEntryPoint(
		t.Context(),
		objName,
		pilotObjectConstants(),
		[]TracingVariantPair{testTracingPair()},
		testFentryProgram,
		"net_recv_lat_event_map",
		DefaultPerfEventBufferBytes,
	)
	if err == nil {
		// Release the probe object at once: it must not stay attached while the
		// object under test is live.
		require.NoError(t, reader.Close(), "closing the probe reader must succeed")
		require.NoError(t, object.Close(), "releasing the probe object must succeed")

		return true
	}

	if IsTracingTargetUnsupported(err) {
		return false
	}

	skipUnsupportedLoad(t, err)
	t.Fatalf("the fentry entry point failed for a reason that is not a missing capability: %v", err)

	return false
}

// TestIsTracingTargetUnsupported keeps the capability verdict narrow: only a
// target the kernel cannot support at all may be reported as such, so that a
// permission or resource failure is never remembered as a missing capability.
func TestIsTracingTargetUnsupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "target missing from the kernel BTF", err: errTracingTargetUnsupported, want: true},
		{name: "wrapped target verdict", err: fmt.Errorf("load: %w", errTracingTargetUnsupported), want: true},
		{name: "kernel refuses the program type", err: fmt.Errorf("load program: %w", ebpf.ErrNotSupported), want: true},
		{name: "permission failure", err: fmt.Errorf("create tracing link: %w", unix.EPERM)},
		{name: "resource failure", err: errors.New("cannot allocate memory")},
		{name: "no error", err: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, IsTracingTargetUnsupported(tt.err))
		})
	}
}

// TestIsPairedEntryPoint guards the name a caller may ask for: a program that
// belongs to no pair would prune both entry points and load an object whose
// hook is silently missing.
func TestIsPairedEntryPoint(t *testing.T) {
	t.Parallel()

	pairs := []TracingVariantPair{testTracingPair()}
	require.True(t, isPairedEntryPoint(pairs, testKprobeProgram))
	require.True(t, isPairedEntryPoint(pairs, testFentryProgram))
	require.False(t, isPairedEntryPoint(pairs, "netif_receive_skb_prog"))
	require.False(t, isPairedEntryPoint(pairs, ""))
	require.False(t, isPairedEntryPoint(nil, testFentryProgram))
}

// TestProbeFentryTargetReportsMissingTarget pins the one verdict the probe
// reaches on its own: a function the kernel BTF does not carry.
func TestProbeFentryTargetReportsMissingTarget(t *testing.T) {
	t.Parallel()

	err := probeFentryTarget("")
	require.Error(t, err)
	require.True(t, IsTracingTargetUnsupported(err), "an empty target is unusable: %v", err)

	if err := features.HaveProgramType(ebpf.Tracing); err != nil {
		t.Skipf("skipping: the kernel offers no tracing program type: %v", err)
	}

	// probeFentryTarget reads the kernel BTF itself, so a name no kernel
	// defines must come back as a target verdict, not as a diagnostic.
	err = probeFentryTarget("huatuo_no_such_target_function")
	require.Error(t, err)
	require.True(t, IsTracingTargetUnsupported(err), "a missing function is a target verdict: %v", err)
	require.Contains(t, err.Error(), "kernel BTF", "the verdict must name the BTF lookup: %v", err)
}

// TestLoadAttachAndEventPipeForEntryPointAttemptsOnlyRequestedProgram loads each
// entry point alone against the running kernel: the object must carry that
// program with exactly one link, must not carry its twin, and must come back
// attached.
func TestLoadAttachAndEventPipeForEntryPointAttemptsOnlyRequestedProgram(t *testing.T) {
	requireBPFPermission(t)

	tests := []struct {
		name    string
		program string
		dropped string
	}{
		{name: "kprobe alone", program: testKprobeProgram, dropped: testFentryProgram},
		{name: "fentry alone", program: testFentryProgram, dropped: testKprobeProgram},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objName, _ := pilotEntryPointObject(t)

			object, reader, err := LoadAttachAndEventPipeForEntryPoint(
				t.Context(),
				objName,
				pilotObjectConstants(),
				[]TracingVariantPair{testTracingPair()},
				tt.program,
				"net_recv_lat_event_map",
				DefaultPerfEventBufferBytes,
			)
			if err != nil {
				skipUnsupportedLoad(t, err)
				t.Fatalf("LoadAttachAndEventPipeForEntryPoint(%q) error = %v, want nil", tt.program, err)
			}

			loaded, _ := pilotObjectCounts(t, object)
			require.Equal(t, 1, loaded[tt.program], "the requested entry point must carry exactly one link")
			require.NotContains(t, loaded, tt.dropped, "the other entry point must not be loaded")
			require.Equal(t, 1, loaded["netif_receive_skb_prog"], "tracepoint programs must survive")
			require.Equal(t, 1, loaded["skb_copy_datagram_iovec_prog"], "tracepoint programs must survive")

			require.NoError(t, reader.Close(), "closing the reader must succeed")
			require.NoError(t, object.Close(), "closing the object must succeed")

			// A closed object tracks no program and holds no link, so a
			// tracer that reloads cannot inherit a stale attachment.
			released, links := pilotObjectCounts(t, object)
			require.Empty(t, released, "a closed object must not track programs")
			require.Zero(t, links, "a closed object must hold no link")
		})
	}
}

// TestLoadAttachAndEventPipeForEntryPointRejectsUnknownProgram checks that a
// name belonging to no pair stops the load before pruning can delete the hook.
func TestLoadAttachAndEventPipeForEntryPointRejectsUnknownProgram(t *testing.T) {
	requireBPFPermission(t)

	objName, _ := pilotEntryPointObject(t)

	_, _, err := LoadAttachAndEventPipeForEntryPoint(
		t.Context(),
		objName,
		pilotObjectConstants(),
		[]TracingVariantPair{testTracingPair()},
		"netif_receive_skb_prog",
		"net_recv_lat_event_map",
		DefaultPerfEventBufferBytes,
	)
	require.Error(t, err)
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

// stuckLink is a link the kernel refuses to close, which is the state a link
// is in when detaching it fails. The embedded interface carries the methods
// this test never calls; only Close is part of the behavior under test.
type stuckLink struct {
	link.Link

	err error
}

func (l *stuckLink) Close() error { return l.err }

// TestAttachFailureStopsTheTracingFallback covers the attach half of the crash
// window: a program is attached, the next one fails to attach, and the link of
// the attached program cannot be closed. The failure must reach the caller as
// an unreleased attempt, because the coordinator loading the kprobe entry point
// over a live fentry link is the double attach this change exists to prevent.
func TestAttachFailureStopsTheTracingFallback(t *testing.T) {
	t.Parallel()

	// The program already carries a link, so this is an attach that failed
	// after a successful one. A tracing program without a handle fails in the
	// parse step, before anything reaches the kernel.
	attached := &loadedProgram{
		name:          testFentryProgram,
		programType:   ebpf.Tracing,
		sectionName:   "fentry/" + testVariantTarget,
		sectionPrefix: "fentry",
		attachTo:      testVariantTarget,
		links: map[string]link.Link{
			testVariantTarget: &stuckLink{err: errInjectedClose},
		},
	}

	object := &defaultBPF{
		programsByID:     map[uint32]*loadedProgram{1: attached},
		programIDsByName: map[string]uint32{attached.name: 1},
	}

	err := object.Attach()
	require.Error(t, err, "attaching a program without a handle must fail")
	require.ErrorIs(t, err, errInjectedClose, "the detach error must survive")
	require.True(t, isTracingCleanupFailure(err),
		"an attach that could not release a link must be marked: %v", err)

	// The link is still attached, so it must still be known. Close() has to
	// report it instead of returning success over a live hook.
	require.Contains(t, attached.links, testVariantTarget,
		"a link that would not close must stay tracked")
	require.ErrorIs(t, object.Close(), errInjectedClose,
		"Close() must report the link it could not detach")

	// The coordinator sees exactly this error and must not retry over it.
	attempts := 0
	attempt := func(context.Context, *ebpf.CollectionSpec) (BPF, PerfEventReader, error) {
		attempts++

		return nil, nil, err
	}

	_, _, _, fallbackErr := loadTracingVariantWithFallback(
		t.Context(),
		newTracingTestSpec(),
		[]TracingVariantPair{testTracingPair()},
		probeResult(nil),
		attempt,
	)
	require.Error(t, fallbackErr)
	require.Equal(t, 1, attempts,
		"the fallback must not run while a link may still be attached")
}

// TestClassifyTracingLoadFailure covers the verdict a failed tracing load
// carries: the kernel saying it does not know the program type is a missing
// capability, everything else stays the load error it was.
func TestClassifyTracingLoadFailure(t *testing.T) {
	t.Parallel()

	var (
		errUnknownType = fmt.Errorf("program tcp_v4_rcv_fentry_prog: load program: %w", unix.EINVAL)
		errTooBig      = fmt.Errorf("program tcp_v4_rcv_fentry_prog: load program: %w", unix.E2BIG)
		errVerifier    = fmt.Errorf("program tcp_v4_rcv_fentry_prog: load program: %w", unix.EINVAL)
		errPermission  = fmt.Errorf("program tcp_v4_rcv_fentry_prog: load program: %w", unix.EPERM)
	)

	tests := []struct {
		name         string
		err          error
		probeErr     error
		wantUnmarked bool
	}{
		{
			name:         "no error stays no error",
			err:          nil,
			probeErr:     ebpf.ErrNotSupported,
			wantUnmarked: true,
		},
		{
			name:     "a kernel without the program type cannot support the target",
			err:      errUnknownType,
			probeErr: ebpf.ErrNotSupported,
		},
		{
			name:     "an attribute the kernel does not know is the same verdict",
			err:      errTooBig,
			probeErr: ebpf.ErrNotSupported,
		},
		{
			name:         "a verifier rejection is an ordinary failure",
			err:          errVerifier,
			probeErr:     nil,
			wantUnmarked: true,
		},
		{
			name:         "a permission failure is an ordinary failure",
			err:          errPermission,
			probeErr:     ebpf.ErrNotSupported,
			wantUnmarked: true,
		},
		{
			name:         "a probe that cannot answer keeps the load error",
			err:          errUnknownType,
			probeErr:     errProbeTransient,
			wantUnmarked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			probe := func() error { return tt.probeErr }

			got := classifyTracingLoadFailure(tt.err, probe)

			if tt.err == nil {
				require.NoError(t, got)

				return
			}

			// The load error itself must survive either way: the verdict is
			// added to it, never a replacement for it.
			require.ErrorIs(t, got, tt.err)
			require.Equal(t, tt.wantUnmarked, !IsTracingTargetUnsupported(got),
				"IsTracingTargetUnsupported(%v) = %t, want unmarked %t",
				got, !tt.wantUnmarked, tt.wantUnmarked)
		})
	}
}

// TestClassifyEntryPointLoadFailure covers the same verdict for a load that
// names one entry point: only the fentry side can be read as a missing kernel
// capability, because the program type probe describes the tracing program type
// and says nothing about a kprobe load that failed the same way.
func TestClassifyEntryPointLoadFailure(t *testing.T) {
	t.Parallel()

	var (
		errFentryEINVAL = fmt.Errorf("program %s: load program: %w", testFentryProgram, unix.EINVAL)
		errKprobeEINVAL = fmt.Errorf("program %s: load program: %w", testKprobeProgram, unix.EINVAL)
	)

	tests := []struct {
		name         string
		err          error
		entryPoint   string
		probeErr     error
		wantUnmarked bool
	}{
		{
			name:       "the fentry entry point a kernel without the program type rejects",
			err:        errFentryEINVAL,
			entryPoint: testFentryProgram,
			probeErr:   ebpf.ErrNotSupported,
		},
		{
			name:         "a kprobe failure keeps the meaning it already had",
			err:          errKprobeEINVAL,
			entryPoint:   testKprobeProgram,
			probeErr:     ebpf.ErrNotSupported,
			wantUnmarked: true,
		},
		{
			name:         "the fentry entry point of a kernel that knows the program type",
			err:          errFentryEINVAL,
			entryPoint:   testFentryProgram,
			probeErr:     nil,
			wantUnmarked: true,
		},
		{
			name:         "an entry point of no pair stays a load error",
			err:          errFentryEINVAL,
			entryPoint:   "tcp_v4_rcv_unpaired_prog",
			probeErr:     ebpf.ErrNotSupported,
			wantUnmarked: true,
		},
	}

	pairs := []TracingVariantPair{testTracingPair()}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			probe := func() error { return tt.probeErr }

			got := classifyEntryPointLoadFailure(tt.err, pairs, tt.entryPoint, probe)

			require.ErrorIs(t, got, tt.err)
			require.Equal(t, tt.wantUnmarked, !IsTracingTargetUnsupported(got),
				"IsTracingTargetUnsupported(%v) = %t, want unmarked %t",
				got, !tt.wantUnmarked, tt.wantUnmarked)
		})
	}
}
