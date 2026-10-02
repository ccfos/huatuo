// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/core/metrics/mthreads/mtml"
)

// --- pure-function tests ----------------------------------------------------

func TestRecordStaticRead_FiltersNilAndNotSupported(t *testing.T) {
	// nil errors and NotSupported errors must NOT be appended to errs:
	// the cache-publication path relies on this so a device that simply
	// lacks a static field (or driver version) does not block cache.
	var errs []error
	recordStaticRead(0, "name", nil, &errs)
	recordStaticRead(0, "pci", mtml.MakeError("mtmlDeviceGetPciInfo", mtml.ErrorNotSupported), &errs)
	if len(errs) != 0 {
		t.Fatalf("recordStaticRead appended %d errors for nil/NotSupported, want 0", len(errs))
	}
	if !mtml.IsNotSupported(mtml.MakeError("mtmlDeviceGetPciInfo", mtml.ErrorNotSupported)) {
		t.Fatal("MakeError(ErrorNotSupported) does not pass IsNotSupported filter")
	}

	recordStaticRead(2, "bios", errors.New("transient"), &errs)
	if len(errs) != 1 {
		t.Fatalf("recordStaticRead did not append transient error, len=%d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "gpu 2 bios") {
		t.Fatalf("error message lacks gpu/name prefix: %v", errs[0])
	}
}

func TestRecordStaticRead_DoesNotPanicOnEmptyName(t *testing.T) {
	// Defensive: an empty name should still produce a reasonable error
	// string, not panic. This guards against a future call site that
	// passes "" by mistake.
	var errs []error
	recordStaticRead(0, "", errors.New("oops"), &errs)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
}

func TestMemoryTypeName(t *testing.T) {
	cases := map[uint32]string{
		mtml.MemoryTypeLPDDR4: "LPDDR4",
		mtml.MemoryTypeGDDR6:  "GDDR6",
		999:                   "unknown",
	}
	for in, want := range cases {
		if got := memoryTypeName(in); got != want {
			t.Errorf("memoryTypeName(%d) = %q, want %q", in, got, want)
		}
	}
}

// --- collector state-machine tests (no mtml calls) -------------------------

// TestInvalidateCache_ClearsStoredCache verifies that after invalidateCache,
// the next getOrBuildCache will rebuild from scratch.
func TestInvalidateCache_ClearsStoredCache(t *testing.T) {
	c := &mthreadsGpuCollector{}
	c.cache.Store(&mthreadsCache{devices: []uint32{0, 1, 2}})

	c.invalidateCache()
	if c.cache.Load() != nil {
		t.Fatal("invalidateCache did not clear the stored cache")
	}
}

// TestGetOrBuildCache_DoubleCheckLock guards the fast-path under the
// framework's "one Update at a time" contract. We pre-load the cache so
// the fast path returns without ever calling buildCache; the test
// fails if the lock is taken in a way that races.
func TestGetOrBuildCache_DoubleCheckLock(t *testing.T) {
	c := &mthreadsGpuCollector{}
	want := &mthreadsCache{devices: []uint32{0, 1, 2}}
	c.cache.Store(want)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			got, err := c.getOrBuildCache(context.Background())
			if err != nil {
				t.Errorf("getOrBuildCache returned err: %v", err)
				return
			}
			if got != want {
				t.Errorf("got %p, want %p", got, want)
			}
		}()
	}
	wg.Wait()
}

// --- config transition tests (cfg-only, no mtml calls) ---------------------

// TestMthreadsConfigTransitions_AllDisabled_AfterSet verifies the
// zero-value Config disables every Mthreads sub-flag.
func TestMthreadsConfigTransitions_AllDisabled_AfterSet(t *testing.T) {
	cfg := &Config{}
	Set(cfg)
	got := configSnapshot()
	if got.Mthreads.EnableHealth {
		t.Fatal("EnableHealth is true after Set with zero-value config")
	}
	if got.Mthreads.EnablePCIe {
		t.Fatal("EnablePCIe is true after Set with zero-value config")
	}
	if got.Mthreads.EnableMTLink {
		t.Fatal("EnableMTLink is true after Set with zero-value config")
	}
}

// TestMthreadsConfigTransitions_FlagFlips verifies that flipping each
// sub-flag survives the Set/snapshot round-trip atomically. The
// collector gates PCIe/MTLink/Health on these flags at scrape time, so a
// stale snapshot would silently drop metric groups.
func TestMthreadsConfigTransitions_FlagFlips(t *testing.T) {
	for _, flip := range []bool{true, false, true, false} {
		c := &Config{}
		c.Mthreads.EnableHealth = flip
		c.Mthreads.EnablePCIe = flip
		c.Mthreads.EnableMTLink = flip
		Set(c)
		got := configSnapshot()
		if got.Mthreads.EnableHealth != flip ||
			got.Mthreads.EnablePCIe != flip ||
			got.Mthreads.EnableMTLink != flip {
			t.Fatalf("Set(%v) round-tripped as %+v", flip, got.Mthreads)
		}
	}
}

// --- joined-error classification --------------------------------------------

// TestIsRecoverable_JoinsAcrossLeaves mirrors the errors.Join tree that
// mthreadsGpuCollector.collect() builds (per-device workers each append
// to a shared errs slice). A single recoverable leaf must trigger
// recovery in Update().
func TestIsRecoverable_JoinsAcrossLeaves(t *testing.T) {
	joined := errors.Join(
		fmt.Errorf("gpu 0: name: %w", mtml.MakeError("mtmlDeviceGetName", mtml.ErrorInvalidArgument)),
		fmt.Errorf("gpu 0: pci sbdf: %w", mtml.MakeError("mtmlDeviceGetPciInfo", mtml.ErrorDriverFailure)),
		fmt.Errorf("gpu 0: bios: %w", mtml.MakeError("mtmlDeviceGetMtBiosVersion", mtml.ErrorNotFound)),
	)
	if !mtml.IsRecoverable(joined) {
		t.Fatal("IsRecoverable(joined with one recoverable leaf) = false, want true")
	}
}

// --- poison state-machine tests (no mtml calls) ----------------------------
//
// setPoisoned is the gate that turns a one-time Shutdown failure into a
// permanent "GPU metrics disabled" short-circuit. The tests below pin the
// invariants that Update() relies on:
//   1. The first poison wins; subsequent calls are no-ops so the original
//      root cause is preserved.
//   2. poisonedErr() returns a Join tree that keeps the root cause reachable
//      via errors.Is — upstream alerts can still classify the underlying
//      MTML error code (e.g. ErrorDriverFailure) even though the
//      Update() caller only sees the wrapped error.
//   3. The two atomic stores happen in the documented order: poisonErr
//      first, then poisoned=true. A reordering would let Update() see
//      poisoned=true with a nil poisonErr, dropping the root cause.

// TestSetPoisoned_IsIdempotent verifies that the first setPoisoned wins
// and subsequent calls are dropped. Repeated calls (which the framework
// currently cannot produce because Update() is serialized, but a future
// health-check hook might) must NOT overwrite the original root cause.
func TestSetPoisoned_IsIdempotent(t *testing.T) {
	c := &mthreadsGpuCollector{}

	first := mtml.MakeError("mtmlLibraryShutDown", mtml.ErrorUnknown)
	c.setPoisoned(first)

	if !c.poisoned.Load() {
		t.Fatal("poisoned.Load() = false after first setPoisoned")
	}
	if ep := c.poisonErr.Load(); ep == nil || !errors.Is(*ep, first) {
		t.Fatalf("poisonErr = %v, want %v", ep, first)
	}

	// Second call with a different error must be ignored: the original
	// root cause is preserved, and the function does not panic.
	second := mtml.MakeError("mtmlLibraryInit", mtml.ErrorDriverFailure)
	c.setPoisoned(second)

	if !c.poisoned.Load() {
		t.Fatal("poisoned.Load() = false after second setPoisoned")
	}
	if ep := c.poisonErr.Load(); ep == nil || !errors.Is(*ep, first) {
		t.Fatalf("poisonErr overwritten by second setPoisoned: got %v, want %v", ep, first)
	}
}

// TestPoisonedErr_ChainsRootCause verifies that the error returned by the
// short-circuit path keeps the original MTML error reachable via
// errors.Is + errors.As. Upstream alerts classify on the inner *Error;
// losing the chain would silently mask the failure mode.
func TestPoisonedErr_ChainsRootCause(t *testing.T) {
	c := &mthreadsGpuCollector{}
	root := mtml.MakeError("mtmlLibraryShutDown", mtml.ErrorDriverFailure)
	c.setPoisoned(root)

	got := c.poisonedErr()
	if got == nil {
		t.Fatal("poisonedErr() = nil after setPoisoned")
	}

	// errors.Is must still find the root cause inside the Join tree.
	if !errors.Is(got, root) {
		t.Fatalf("errors.Is(poisonedErr, root) = false; err = %v", got)
	}

	// And errors.As must extract the inner *Error so callers can read
	// the MTML return code.
	var inner *mtml.Error
	if !errors.As(got, &inner) {
		t.Fatalf("errors.As(poisonedErr, *mtml.Error) = false; err = %v", got)
	}
	if !errors.Is(inner, root) {
		t.Fatalf("inner *mtml.Error = %v, want %v", inner, root)
	}

	// The wrapper must carry the human-readable short-circuit marker so
	// log readers can tell the failure apart from a transient collect
	// error.
	if !strings.Contains(got.Error(), "library poisoned") {
		t.Fatalf("poisonedErr text lacks marker: %q", got.Error())
	}
}

// TestPoisonedErr_NilRootReturnsMarkerOnly covers the "no root cause
// stored" branch: poisonedErr() must still return a non-nil error with
// the marker, not panic. This is reachable if a future caller invokes
// setPoisoned(nil) directly.
func TestPoisonedErr_NilRootReturnsMarkerOnly(t *testing.T) {
	c := &mthreadsGpuCollector{}
	c.poisoned.Store(true) // skip setPoisoned to test the unguarded branch
	// poisonErr is left nil deliberately.

	got := c.poisonedErr()
	if got == nil {
		t.Fatal("poisonedErr() = nil with poisoned=true and poisonErr=nil")
	}
	if !strings.Contains(got.Error(), "library poisoned") {
		t.Fatalf("poisonedErr text lacks marker: %q", got.Error())
	}
}
