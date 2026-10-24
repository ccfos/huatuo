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

package v2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/cgroups/stats"

	"github.com/ccfos/huatuo/internal/cgroups/paths"

	"github.com/godbus/dbus/v5"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// The tests below cover the fix in commit 0b86d742: on cgroup v2 with the
// systemd driver, resource updates must go through systemd D-Bus properties
// (CPUQuotaPerSecUSec, MemoryMax) instead of writing directly to cgroupfs,
// otherwise systemd overwrites them on reload.
//
// specToSystemdProperties is the pure translation step from OCI spec to
// systemd unit properties. It is the correctness core of the fix and is
// unit-testable without root or a running systemd.

func i64ptr(v int64) *int64   { return &v }
func u64ptr(v uint64) *uint64 { return &v }

// findProperty returns the dbus.Variant value for the named systemd property,
// or nil if absent. Property order is not part of the contract.
func findProperty(t *testing.T, props []struct {
	Name  string
	Value dbus.Variant
}, name string,
) *dbus.Variant {
	t.Helper()
	for i := range props {
		if props[i].Name == name {
			return &props[i].Value
		}
	}
	return nil
}

func TestSpecToSystemdProperties(t *testing.T) {
	tests := []struct {
		name string
		spec *specs.LinuxResources
		// wantNames lists property names that MUST be present, in any order.
		wantNames []string
		// wantValues maps property name -> expected uint64 value.
		wantValues map[string]uint64
	}{
		{
			// Baseline: 1 core (quota=100000, period=100000) -> 1_000_000 usec/sec.
			// Exact multiple of 10000, so the rounding branch is not taken.
			name: "cpu-one-core-exact",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(100000),
					Period: u64ptr(100000),
				},
			},
			wantNames:  []string{"CPUQuotaPerSecUSec"},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 1000000},
		},
		{
			// 0.5 core -> 500000 usec/sec, also an exact 10000 multiple.
			name: "cpu-half-core-exact",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(50000),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 500000},
		},
		{
			// quota=1, period=100000 -> 10 usec/sec, not a 10000 multiple,
			// so the code must round UP to 10000. This guards the branch
			//   cpuQuotaPerSecUSec = ((cpuQuotaPerSecUSec / 10000) + 1) * 10000
			// which is required by systemd's granularity.
			name: "cpu-round-up-tiny-quota",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(1),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 10000},
		},
		{
			// 12345 usec/period, period=100000 -> 123450 usec/sec, must round
			// up to 130000 (not 120000 — the fix always rounds up when the
			// remainder is non-zero, matching systemd behavior).
			name: "cpu-round-up-non-multiple",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(12345),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 130000},
		},
		{
			// CPU section present but Quota <= 0 must be ignored: systemd
			// treats a zero/negative quota as "unlimited" which would silently
			// erase an existing limit. The fix guards *Quota > 0.
			name: "cpu-zero-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(0),
					Period: u64ptr(100000),
				},
			},
			wantNames: nil,
		},
		{
			name: "cpu-negative-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(-1),
					Period: u64ptr(100000),
				},
			},
			wantNames: nil,
		},
		{
			// Period=0 would divide-by-zero; must be skipped.
			name: "cpu-zero-period-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(50000),
					Period: u64ptr(0),
				},
			},
			wantNames: nil,
		},
		{
			// Nil pointers on optional sub-fields must be tolerated.
			name: "cpu-nil-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{Period: u64ptr(100000)},
			},
			wantNames: nil,
		},
		{
			name: "cpu-nil-period-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{Quota: i64ptr(50000)},
			},
			wantNames: nil,
		},
		{
			// Memory limit maps to MemoryMax (uint64). Signed->unsigned cast
			// mirrors the source.
			name: "memory-only-1gib",
			spec: &specs.LinuxResources{
				Memory: &specs.LinuxMemory{Limit: i64ptr(1 << 30)},
			},
			wantValues: map[string]uint64{"MemoryMax": 1 << 30},
		},
		{
			// Both CPU and Memory populated -> two properties.
			name: "cpu-and-memory",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(200000),
					Period: u64ptr(100000),
				},
				Memory: &specs.LinuxMemory{Limit: i64ptr(512 * 1024 * 1024)},
			},
			wantValues: map[string]uint64{
				"CPUQuotaPerSecUSec": 2000000,
				"MemoryMax":          512 * 1024 * 1024,
			},
		},
		{
			// Memory section present but Limit nil must not emit MemoryMax.
			name: "memory-nil-limit-skipped",
			spec: &specs.LinuxResources{
				Memory: &specs.LinuxMemory{},
			},
			wantNames: nil,
		},
		{
			// Empty spec -> no properties. This is what makes UpdateRuntime
			// safe to skip the D-Bus call entirely.
			name:      "empty-spec",
			spec:      &specs.LinuxResources{},
			wantNames: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := specToSystemdProperties(tc.spec)

			// Normalise to a name/value list independent of the concrete
			// systemdDbus.Property struct so this test does not import the
			// systemd package just for reflection.
			normalised := make([]struct {
				Name  string
				Value dbus.Variant
			}, len(got))
			for i, p := range got {
				normalised[i].Name = p.Name
				normalised[i].Value = p.Value
			}

			// Combine expected names: explicit wantNames plus every key in
			// wantValues.
			want := map[string]bool{}
			for _, n := range tc.wantNames {
				want[n] = true
			}
			for n := range tc.wantValues {
				want[n] = true
			}

			if len(normalised) != len(want) {
				t.Fatalf("property count: got %d %v, want %d %v",
					len(normalised), propNames(normalised), len(want), keys(want))
			}

			for name, wantVal := range tc.wantValues {
				v := findProperty(t, normalised, name)
				if v == nil {
					t.Fatalf("property %q: missing, got %v", name, propNames(normalised))
				}
				var gotVal uint64
				if err := v.Store(&gotVal); err != nil {
					t.Fatalf("property %q: decode variant: %v", name, err)
				}
				if gotVal != wantVal {
					t.Errorf("property %q: got %d, want %d", name, gotVal, wantVal)
				}
			}
		})
	}
}

func propNames(props []struct {
	Name  string
	Value dbus.Variant
},
) []string {
	names := make([]string, len(props))
	for i, p := range props {
		names[i] = p.Name
	}
	return names
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUpdateRuntimeEmptySpecNoDbusCall verifies the short-circuit in
// UpdateRuntime: when the spec produces no properties, the function must
// return nil without attempting to open a systemd D-Bus connection. This
// matters because UpdateRuntime is called opportunistically and must not
// fail on hosts where systemd is absent (e.g. minimal test containers).
func TestUpdateRuntimeEmptySpecNoDbusCall(t *testing.T) {
	c := &CgroupV2{name: "unified", unitName: "does-not-matter.slice"}

	if err := c.UpdateRuntime(&specs.LinuxResources{}); err != nil {
		t.Fatalf("UpdateRuntime(empty): got %v, want nil (must short-circuit before D-Bus)", err)
	}

	// Memory section without Limit is also empty in property terms.
	if err := c.UpdateRuntime(&specs.LinuxResources{Memory: &specs.LinuxMemory{}}); err != nil {
		t.Fatalf("UpdateRuntime(empty memory): got %v, want nil", err)
	}

	// CPU section with invalid quota/period must also be a no-op.
	if err := c.UpdateRuntime(&specs.LinuxResources{
		CPU: &specs.LinuxCPU{Quota: i64ptr(0), Period: u64ptr(100000)},
	}); err != nil {
		t.Fatalf("UpdateRuntime(zero quota): got %v, want nil", err)
	}
}

// TestPostfixConstant pins the systemd unit suffix. The fix in NewRuntime
// stores `path + Postfix` as unitName; downstream D-Bus calls target that
// exact unit name, so a change here would silently break UpdateRuntime.
func TestPostfixConstant(t *testing.T) {
	if Postfix != ".slice" {
		t.Errorf("Postfix: got %q, want %q", Postfix, ".slice")
	}
}

func TestCpuQuotaAndPeriodEffectiveCPUCount(t *testing.T) {
	root := t.TempDir()
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	tests := []struct {
		name      string
		effective string
		requested string
		want      uint64
	}{
		{name: "effective", effective: "0-3", requested: "0-7", want: 4},
		{name: "requested ignored", requested: "0-1", want: 0},
		{name: "missing", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("test", tt.name)
			writeCgroupFile(t, paths.Path(path, "cpu.max"), "max 100000\n")
			if tt.effective != "" {
				writeCgroupFile(t, paths.Path(path, "cpuset.cpus.effective"), tt.effective+"\n")
			}
			if tt.requested != "" {
				writeCgroupFile(t, paths.Path(path, "cpuset.cpus"), tt.requested+"\n")
			}

			quota, err := (&CgroupV2{}).CpuQuotaAndPeriod(path)
			if err != nil {
				t.Fatal(err)
			}
			if quota.EffectiveCPUCount != tt.want {
				t.Errorf("EffectiveCPUCount = %d, want %d", quota.EffectiveCPUCount, tt.want)
			}
		})
	}
}

func writeCgroupFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A recording collector keeps time and failures controllable without BPF/root.
type loadStatsFixture struct {
	capacity uint32
	calls    [][]uint64
	scan     func([]uint64) error
	closeFn  func() error
	closed   int
}

func (f *loadStatsFixture) Snapshot(ids []uint64) (map[uint64]stats.LoadStats, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	if f.scan != nil {
		if err := f.scan(ids); err != nil {
			return nil, err
		}
	}
	out := make(map[uint64]stats.LoadStats, len(ids))
	for _, id := range ids {
		out[id] = stats.LoadStats{NrRunning: uint64(len(f.calls))}
	}
	return out, nil
}
func (f *loadStatsFixture) Capacity() uint32 { return f.capacity }
func (f *loadStatsFixture) Close() error {
	f.closed++
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

func loadStatsSnapshot(t *testing.T, s *sharedTaskLoadSnapshotter, consumer LoadStatsConsumer, ids ...uint64) map[uint64]stats.LoadStats {
	t.Helper()
	result, err := s.Snapshot(consumer, ids)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSharedLoadStatsGenerations(t *testing.T) {
	f := &loadStatsFixture{}
	now := time.Unix(100, 0)
	s := &sharedTaskLoadSnapshotter{snapshotter: f, now: func() time.Time { return now }}
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1, 2)
	if got := loadStatsSnapshot(t, s, LoadStatsConsumerDload, 2); got[2].NrRunning != 1 || len(f.calls) != 1 {
		t.Fatalf("second consumer did not reuse the first generation: %v calls=%v", got, f.calls)
	}
	if got := loadStatsSnapshot(t, s, LoadStatsConsumerDload, 2); got[2].NrRunning != 2 || len(f.calls) != 2 {
		t.Fatalf("same consumer reused a generation: %v calls=%v", got, f.calls)
	}
	if got := loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1); got[1].NrRunning != 2 || len(f.calls) != 2 {
		t.Fatalf("first consumer missed the newer generation: %v calls=%v", got, f.calls)
	}
}

func TestSharedLoadStatsReuseDeadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		calls int
	}{
		{"fresh", 100*time.Millisecond - time.Nanosecond, 1},
		{"exact-expiry", 100 * time.Millisecond, 2},
		{"expired", 101 * time.Millisecond, 2},
		{"backwards-clock", -time.Nanosecond, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &loadStatsFixture{}
			now := time.Unix(100, 0)
			s := &sharedTaskLoadSnapshotter{snapshotter: f, now: func() time.Time { return now }}
			loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1)
			now = now.Add(tc.age)
			loadStatsSnapshot(t, s, LoadStatsConsumerDload, 1)
			if len(f.calls) != tc.calls {
				t.Fatalf("scans=%d want=%d", len(f.calls), tc.calls)
			}
		})
	}
	t.Run("scan-time-counts-towards-expiry", func(t *testing.T) {
		now := time.Unix(100, 0)
		f := &loadStatsFixture{scan: func([]uint64) error { now = now.Add(100 * time.Millisecond); return nil }}
		s := &sharedTaskLoadSnapshotter{snapshotter: f, now: func() time.Time { return now }}
		loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1)
		loadStatsSnapshot(t, s, LoadStatsConsumerDload, 1)
		if len(f.calls) != 2 {
			t.Fatal("slow scan extended the reuse deadline")
		}
	})
}

func TestSharedLoadStatsTargetsAndForget(t *testing.T) {
	f := &loadStatsFixture{}
	s := &sharedTaskLoadSnapshotter{snapshotter: f, now: func() time.Time { return time.Unix(100, 0) }}
	ids := []uint64{1, 2, 2}
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, ids...)
	ids[0] = 99 // Caller-owned input cannot change another consumer's target union.
	loadStatsSnapshot(t, s, LoadStatsConsumerDload, 2, 3)
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 4)
	want := [][]uint64{{1, 2}, {1, 2, 3}, {4, 2, 3}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("target scans=%v want=%v", f.calls, want)
	}
	s.Forget(LoadStatsConsumerDload)
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 4)
	if !slices.Equal(f.calls[len(f.calls)-1], []uint64{4}) {
		t.Fatal("forgotten targets remain in scans")
	}
	s.Forget(LoadStatsConsumerLoadavg)
	if len(s.consumers) != 0 || s.generation != 0 || s.snapshot != nil || s.snapshotIDs != nil || !s.sampledAt.IsZero() {
		t.Fatal("last consumer did not clear cached state")
	}
	calls := len(f.calls)
	if got := loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg); len(got) != 0 || len(f.calls) != calls {
		t.Fatal("empty request scanned tasks")
	}
	if _, err := s.Snapshot(0, []uint64{1}); err == nil {
		t.Fatal("invalid consumer accepted")
	}
}

func TestSharedLoadStatsFailedScanDoesNotPublish(t *testing.T) {
	f := &loadStatsFixture{}
	s := &sharedTaskLoadSnapshotter{snapshotter: f, now: func() time.Time { return time.Unix(100, 0) }}
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1)
	generation, at := s.generation, s.sampledAt
	failure := errors.New("scan failed")
	f.scan = func([]uint64) error { return failure }
	if got, err := s.Snapshot(LoadStatsConsumerDload, []uint64{2}); !errors.Is(err, failure) || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if s.generation != generation || !s.sampledAt.Equal(at) || s.snapshot[1].NrRunning != 1 || s.consumers[LoadStatsConsumerDload].lastGeneration != 0 {
		t.Fatal("failed scan published or consumed a generation")
	}
	f.scan = nil
	got := loadStatsSnapshot(t, s, LoadStatsConsumerDload, 2)
	if s.generation != generation+1 || got[2].NrRunning != 3 || len(f.calls) != 3 {
		t.Fatal("failed scan was cached instead of retried")
	}
}

func TestLazyLoadStatsResizeRecovery(t *testing.T) {
	old := &loadStatsFixture{capacity: 128}
	next := &loadStatsFixture{capacity: 256}
	failure := errors.New("allocation failed")
	var attempts int
	s := &lazyTaskLoadSnapshotter{newCollector: func(capacity uint32) (loadCollector, error) {
		attempts++
		switch attempts {
		case 1:
			if capacity != 128 {
				t.Fatalf("initial capacity=%d", capacity)
			}
			return old, nil
		case 2:
			return nil, failure
		default:
			if capacity != 256 {
				t.Fatalf("resized capacity=%d", capacity)
			}
			return next, nil
		}
	}}
	if _, err := s.Snapshot([]uint64{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(make([]uint64, 129)); !errors.Is(err, failure) {
		t.Fatalf("resize err=%v", err)
	}
	if old.closed != 0 || s.collector != old {
		t.Fatal("failed resize discarded working collector")
	}
	if _, err := s.Snapshot([]uint64{1}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatal("small request needlessly reallocated after resize failure")
	}
	if _, err := s.Snapshot(make([]uint64, 129)); err != nil {
		t.Fatal(err)
	}
	if old.closed != 1 || s.collector != next || len(next.calls) != 1 {
		t.Fatal("successful resize did not replace collector")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if next.closed != 1 || s.collector != nil {
		t.Fatal("close did not release the collector exactly once")
	}
}

func TestLazyLoadStatsErrorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  error
		attempts int
	}{
		{"ordinary-retry", errors.New("missing object"), 2},
		{"unsupported-cache", fmt.Errorf("kernel: %w", ErrTaskIteratorNotSupported), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			f := &loadStatsFixture{capacity: 128}
			s := &lazyTaskLoadSnapshotter{newCollector: func(uint32) (loadCollector, error) { attempts++; return nil, tc.failure }}
			for range 2 {
				if _, err := s.Snapshot([]uint64{1}); !errors.Is(err, tc.failure) {
					t.Fatalf("err=%v", err)
				}
			}
			if attempts != tc.attempts {
				t.Fatalf("attempts=%d want=%d", attempts, tc.attempts)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s.newCollector = func(uint32) (loadCollector, error) { attempts++; return f, nil }
			if _, err := s.Snapshot([]uint64{1}); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 1 || s.unsupportedErr != nil {
				t.Fatal("close did not reset support cache")
			}
		})
	}
	verifier := &ebpf.VerifierError{}
	if isTaskIteratorLoadUnsupported(errors.Join(ebpf.ErrNotSupported, verifier)) {
		t.Fatal("verifier error classified as unsupported")
	}
}

func TestLoadStatsCapacityBounds(t *testing.T) {
	for _, tc := range []struct {
		entries int
		want    uint32
	}{
		{1, 128}, {128, 128}, {129, 256}, {65536, 65536}, {65537, 65537},
	} {
		got, err := loadStatsMapCapacity(tc.entries)
		if err != nil || got != tc.want {
			t.Fatalf("capacity(%d)=%d err=%v want=%d", tc.entries, got, err, tc.want)
		}
	}
	if _, err := loadStatsMapCapacity(65538); err == nil {
		t.Fatal("capacity limit not enforced")
	}
}

func TestLoadStatsCloseSerializesWithScan(t *testing.T) {
	for _, kind := range []string{"shared", "lazy"} {
		t.Run(kind, func(t *testing.T) {
			entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f := &loadStatsFixture{capacity: 128, scan: func([]uint64) error { close(entered); <-release; return nil }, closeFn: func() error { close(closed); return nil }}
			var snapshot func() error
			var closeCollector func() error
			var unlocked func() bool
			if kind == "shared" {
				s := &sharedTaskLoadSnapshotter{snapshotter: f}
				snapshot = func() error { _, err := s.Snapshot(LoadStatsConsumerLoadavg, []uint64{1}); return err }
				closeCollector = s.Close
				unlocked = func() bool {
					if s.mu.TryLock() {
						s.mu.Unlock()
						return true
					}
					return false
				}
			} else {
				s := &lazyTaskLoadSnapshotter{newCollector: func(uint32) (loadCollector, error) { return f, nil }}
				snapshot = func() error { _, err := s.Snapshot([]uint64{1}); return err }
				closeCollector = s.Close
				unlocked = func() bool {
					if s.mu.TryLock() {
						s.mu.Unlock()
						return true
					}
					return false
				}
			}
			scanDone := make(chan error, 1)
			go func() { scanDone <- snapshot() }()
			<-entered
			if unlocked() {
				close(release)
				<-scanDone
				t.Fatal("scan does not own lifecycle lock")
			}
			closeStarted, closeDone := make(chan struct{}), make(chan error, 1)
			go func() { close(closeStarted); closeDone <- closeCollector() }()
			<-closeStarted
			select {
			case <-closed:
				close(release)
				<-scanDone
				<-closeDone
				t.Fatal("collector closed during scan")
			default:
			}
			close(release)
			if err := <-scanDone; err != nil {
				t.Fatal(err)
			}
			if err := <-closeDone; err != nil {
				t.Fatal(err)
			}
			select {
			case <-closed:
			default:
				t.Fatal("collector was not closed")
			}
			if f.closed != 1 {
				t.Fatalf("close count=%d", f.closed)
			}
		})
	}
}

// A sequential close proves ownership independently of a concurrent scanner.
func TestLoadStatsCloseOwnsLifecycleLock(t *testing.T) {
	for _, kind := range []string{"shared", "lazy"} {
		t.Run(kind, func(t *testing.T) {
			f := &loadStatsFixture{capacity: 128}
			var mu *sync.Mutex
			var closeCollector func() error
			if kind == "shared" {
				s := &sharedTaskLoadSnapshotter{snapshotter: f}
				mu, closeCollector = &s.mu, s.Close
			} else {
				s := &lazyTaskLoadSnapshotter{collector: f}
				mu, closeCollector = &s.mu, s.Close
			}
			f.closeFn = func() error {
				if mu.TryLock() {
					mu.Unlock()
					t.Error("Close does not own lifecycle lock")
				}
				return nil
			}
			if err := closeCollector(); err != nil {
				t.Fatal(err)
			}
			if f.closed != 1 {
				t.Fatalf("close count=%d", f.closed)
			}
		})
	}
}

func TestSharedLoadStatsCloseResetsState(t *testing.T) {
	failure := errors.New("close failed")
	first := &loadStatsFixture{capacity: 128, closeFn: func() error { return failure }}
	second := &loadStatsFixture{capacity: 128}
	attempts := 0
	lazy := &lazyTaskLoadSnapshotter{newCollector: func(uint32) (loadCollector, error) {
		attempts++
		if attempts == 1 {
			return first, nil
		}
		return second, nil
	}}
	s := &sharedTaskLoadSnapshotter{snapshotter: lazy, now: func() time.Time { return time.Unix(100, 0) }}
	loadStatsSnapshot(t, s, LoadStatsConsumerLoadavg, 1)
	loadStatsSnapshot(t, s, LoadStatsConsumerDload, 1)
	if err := s.Close(); !errors.Is(err, failure) {
		t.Fatalf("close err=%v", err)
	}
	if s.generation != 0 || s.snapshot != nil || s.snapshotIDs != nil || len(s.consumers) != 0 || !s.sampledAt.IsZero() || lazy.collector != nil {
		t.Fatal("close failure left shared state or collector published")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got := loadStatsSnapshot(t, s, LoadStatsConsumerDload, 2)
	if s.generation != 1 || got[2].NrRunning != 1 || first.closed != 1 || !reflect.DeepEqual(second.calls, [][]uint64{{2}}) {
		t.Fatal("reinitialized consumer inherited the closed iterator's state")
	}
}

func TestLazyLoadStatsCachesRuntimeUnsupported(t *testing.T) {
	for _, failure := range []error{ErrTaskIteratorNotSupported, errors.New("transient scan failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := &loadStatsFixture{capacity: 128, scan: func([]uint64) error { return failure }}
			s := &lazyTaskLoadSnapshotter{newCollector: func(uint32) (loadCollector, error) { return f, nil }}
			for i := 0; i < 2; i++ {
				if _, err := s.Snapshot([]uint64{1}); !errors.Is(err, failure) {
					t.Fatalf("snapshot error=%v", err)
				}
			}
			want := 2
			if errors.Is(failure, ErrTaskIteratorNotSupported) {
				want = 1
			}
			if len(f.calls) != want {
				t.Fatalf("scans=%d want=%d", len(f.calls), want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot([]uint64{1}); !errors.Is(err, failure) {
				t.Fatalf("recheck error=%v", err)
			}
			if len(f.calls) != want+1 {
				t.Fatal("Close did not permit recheck")
			}
		})
	}
}
