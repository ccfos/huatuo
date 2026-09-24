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
	"reflect"
	"testing"

	"github.com/cilium/ebpf"
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
