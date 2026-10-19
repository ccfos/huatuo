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

package symbol

import (
	"fmt"
	"strings"
	"testing"
)

// benchKsymFixture loads a synthetic kallsyms with n text symbols.
func benchKsymFixture(b *testing.B, n int) {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("ffffffff8%07x T kern_func_%04d", i*0x10, i))
	}
	resetKernelSymbolFixture(b, lines)
}

// benchStack mimics a real kernel stack: frames repeat across events because
// the same hot functions (e.g. net_rx_action, consume_skb) dominate.
func benchStack() []uint64 {
	stack := make([]uint64, KsymPerfStackDepth)
	for i := range stack {
		stack[i] = 0xffffffff81000010 + uint64(i%16)*0x10
	}
	return stack
}

// ksymFrameStringDirect is the pre-cache baseline resolver: floorSym
// lookup plus formatting on every call, with no memoization.
func ksymFrameStringDirect(addr uint64) string {
	sym := ksymTable.floorSym(addr)
	if sym == nil {
		return failFrame("ksym-not-found", "")
	}
	return fmt.Sprintf("%s/+%d %s", sym.Name, addr-sym.Addr, sym.Module)
}

func BenchmarkKsymStackStrsCached(b *testing.B) {
	benchKsymFixture(b, 300000)
	stack := benchStack()
	KsymStackStrs(stack, KsymStackMaxDepth) // warm the table + frame cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frames := KsymStackStrs(stack, KsymStackMaxDepth)
		if len(frames) != len(stack) {
			b.Fatalf("got %d frames", len(frames))
		}
	}
}

// BenchmarkKsymStackStrsUncached measures the pre-cache baseline:
// resolveStack driving the direct floorSym+format resolver, no cache.
func BenchmarkKsymStackStrsUncached(b *testing.B) {
	benchKsymFixture(b, 300000)
	stack := benchStack()
	ensureKsymsLoaded() // load the table outside the timed loop
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := resolveStack(stack, ksymFrameStringDirect, outTypeString)
		if len(out.strings) != len(stack) {
			b.Fatalf("got %d frames", len(out.strings))
		}
	}
}

func TestKsymFrameCacheBounded(t *testing.T) {
	lines := []string{"ffffffff81000000 T kernel_entry"}
	resetKernelSymbolFixture(t, lines)
	for i := 0; i < ksymFrameCacheCap+16; i++ {
		addr := 0xffffffff82000000 + uint64(i)*0x10
		_ = ksymFrameString(addr)
	}
	ksymFrameMu.Lock()
	size := len(ksymFrameCache)
	ksymFrameMu.Unlock()
	if size > ksymFrameCacheCap {
		t.Fatalf("cache size %d exceeds cap %d", size, ksymFrameCacheCap)
	}
	frame := ksymFrameString(0xffffffff81000000)
	if !strings.Contains(frame, "kernel_entry") {
		t.Fatalf("cached resolution broken: got %q (table=%d)", frame, len(ksymTable))
	}
}
